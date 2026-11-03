package web

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/version"
	"github.com/openlinkos/beacon/internal/web/link"
)

type fakeAM struct{}

func (fakeAM) CreateSilence(context.Context, map[string]string, time.Time, time.Time, string, string) (string, error) {
	return "sil-1", nil
}
func (fakeAM) ExpireSilence(context.Context, string) error              { return nil }
func (fakeAM) Silences(context.Context) ([]alertmanager.Silence, error) { return nil, nil }

// fakeBot keeps what the chat would have been told.
type fakeBot struct {
	mu   sync.Mutex
	sent []string
}

func (b *fakeBot) Send(_ context.Context, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, text)
	return nil
}

// told reports whether the chat was told something containing all of parts.
func (b *fakeBot) told(parts ...string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.sent {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(s, p)
		}
		if all {
			return true
		}
	}
	return false
}

func keySigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	groupKey  = `{}:{alertname="ServiceDown",service="git-example-com"}`
	sessionID = "20261102-095500-abcdef-1"
	hostile   = `<script>alert(1)</script>`
	publicURL = "https://beacon.example.com"
)

// env is a bridge with one firing alert on the machine edge-1, a recorded shell session there, and the
// pages' server over both; now is fixed.
type env struct {
	t      *testing.T
	srv    *Server
	st     *store.Store
	b      *bridge.Bridge
	signer *link.Signer
	mux    *http.ServeMux
	now    time.Time
	id     string
	bot    *fakeBot
	cdb    *configdb.Service
}

func newEnv(t *testing.T, lang i18n.Lang) *env {
	t.Helper()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	e := &env{t: t, now: now, bot: &fakeBot{}}
	cfg := &config.Config{Language: string(lang), Location: time.UTC, PublicURL: publicURL, AckTTL: config.Duration(4 * time.Hour),
		Feishu: config.Feishu{MonthlyBudget: 1000}, OnCallByID: map[string]config.OnCaller{},
		Escalation: map[string]config.Policy{"critical": {Channel: "webhook"}, "warning": {Channel: "webhook"}, "info": {Channel: "webhook"}}}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	st.SetClock(func() time.Time { return e.now }) // the store's clock held still with the rest of the env
	e.signer, err = link.NewSigner(bytes.Repeat([]byte{1}, link.KeySize), time.Hour, func() time.Time { return e.now })
	if err != nil {
		t.Fatal(err)
	}
	pages := &link.Pages{Signer: e.signer, PublicURL: publicURL}
	e.b = bridge.New(bridge.Deps{Config: cfg, Store: st, AM: fakeAM{}, Bot: e.bot, Pages: pages, Now: func() time.Time { return e.now }})
	err = e.b.HandleWebhook(context.Background(), alertmanager.Webhook{Version: "4", GroupKey: groupKey, Status: "firing",
		GroupLabels:       map[string]string{"alertname": "ServiceDown", "service": "git-example-com"},
		CommonLabels:      map[string]string{"alertname": "ServiceDown", "service": "git-example-com", "severity": "critical", "host": "edge-1", "note": hostile},
		CommonAnnotations: map[string]string{"summary": "git.example.com is down " + hostile, "description": "No answer from 2 locations"},
		Alerts:            []alertmanager.Alert{{Status: "firing", Fingerprint: "f1", StartsAt: now.Add(-10 * time.Minute)}}})
	if err != nil {
		t.Fatal(err)
	}
	e.id = bridge.GroupID(groupKey)
	if _, err := e.b.AddNote(context.Background(), e.id, bridge.Actor{Name: "claude-code", Via: "mcp"}, "restarting the relay "+hostile); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	gate, err := sshgate.New(sshgate.Deps{Store: st, HostKey: keySigner(t), CA: keySigner(t), Dir: dir, Lang: lang, Pages: pages, Bot: e.bot,
		Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	zero, one := 0, 1
	rec := store.SessionRecord{ID: sessionID, Agent: "claude-code", Via: "ssh", Machine: "edge-1", Kind: "shell",
		Started: now.Add(-5 * time.Minute), Ended: now.Add(-3 * time.Minute), Exit: &zero, Commands: 2, Recording: "2026/11/02/" + sessionID}
	if err := st.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	day := filepath.Join(dir, "2026", "11", "02")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	cast := strings.Join([]string{
		`{"version":3,"term":{"cols":80,"rows":24,"type":"xterm-256color"},"timestamp":1762073700,"command":"","title":"claude-code on edge-1"}`,
		`[0.5,"o","edge-1$ "]`,
		`[1.0,"i","systemctl restart relay\r"]`,
		`[0.1,"m","systemctl restart relay"]`,
		`[0.2,"o","systemctl restart relay\r\nrelay restarted\r\nedge-1$ "]`,
		`[0.3,"r","100x30"]`,
		`[0.4,"i","false\r"]`,
		`[0.1,"m","false"]`,
		`[0.1,"o","false\r\nedge-1$ exit\r\n"]`,
		`[0.2,"x","0"]`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(day, sessionID+".cast"), []byte(cast), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := sshgate.Transcript{SessionRecord: rec, Commands: []sshgate.Command{
		{Command: "systemctl restart relay", Output: "relay restarted", Exit: &zero, Started: rec.Started, Ms: 1200},
		{Command: "false " + hostile, Output: "", Exit: &one, Started: rec.Started.Add(2 * time.Second), Ms: 5},
	}}
	b, _ := json.Marshal(tr)
	if err := os.WriteFile(filepath.Join(day, sessionID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// a second session elsewhere, by someone else: not the alert's business
	other := store.SessionRecord{ID: "20261102-095600-abcdef-1", Agent: "codex", Via: "mcp", Machine: "edge-2", Kind: "exec", Command: "uptime",
		Started: now.Add(-4 * time.Minute), Ended: now.Add(-4 * time.Minute), Exit: &zero}
	_ = st.PutSession(other)

	// the console's: the configuration, machines, an agent's call, the hooks, the gateway's last writes
	e.cdb = configdb.Open(configdb.Deps{Store: st, Lang: lang, Now: func() time.Time { return e.now }})
	alice := configdb.Actor{Name: "alice", Via: "api"}
	for _, loc := range []kv{{"edge-1", `{"name": "Europe - Frankfurt", "region": "overseas"}`}, {"edge-2", `{"name": "Asia - Shanghai", "region": "mainland"}`}} {
		if _, err := e.cdb.Put(context.Background(), alice, "locations", loc.K, json.RawMessage(loc.V), "new location "+hostile); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []store.Machine{
		{Name: "edge-1", Address: "192.0.2.10:22", User: "beacon", Critical: true, Note: "the relay " + hostile, Added: now.Add(-48 * time.Hour), AddedBy: "alice"},
		{Name: "edge-2", Address: store.ConnectorAddress, User: "beacon", Added: now.Add(-24 * time.Hour)},
	} {
		if err := st.PutMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := st.Group(e.id)
	g.Agents = append(g.Agents, store.AgentCall{Hook: "oncall", Kind: "webhook", Event: "firing", Delivery: "d1", Status: "ok", Attempts: 1, Code: 202,
		At: now.Add(-9 * time.Minute), Done: now.Add(-9 * time.Minute)})
	if err := st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutChange(store.Change{Time: now.Add(-20 * time.Minute), Source: "flux", Kind: "deploy", Service: "git-example-com",
		Summary: "relay 1.2.3 " + hostile, PostedBy: "ci"}, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.srv = &Server{Bridge: e.b, SSH: gate, Auth: &api.Auth{Store: st}, Pages: pages, Lang: lang, Location: time.UTC, Now: func() time.Time { return e.now },
		Store: st, Config: e.cdb, Version: "v0.3.0-test",
		Hooks: []config.Agent{{Name: "oncall", Kind: "webhook", URL: "https://hooks.example.com/beacon?key=hook-secret", Severities: []string{"critical"}}},
		Reporting: func() map[string]time.Time {
			return map[string]time.Time{"edge-1": e.now.Add(-time.Minute), "edge-2": e.now.Add(-time.Hour)}
		}}
	e.mux = http.NewServeMux()
	e.srv.Register(e.mux)
	return e
}

// link is the signed relative link to path, as the pages hand them out.
func (e *env) link(path string) string { return path + "?" + e.signer.Query(path) }

func (e *env) get(path, token string) (int, string, http.Header) {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b), rec.Header()
}

func (e *env) token(name string, scopes ...string) string {
	e.t.Helper()
	secret, _, err := e.st.CreateToken(name, scopes, time.Hour, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return secret
}

var hrefRe = regexp.MustCompile(`href="([^"]+)"`)

// hrefs lists the links in a page that start with prefix, unescaped.
func hrefs(body, prefix string) []string {
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
		if u := html.UnescapeString(m[1]); strings.HasPrefix(u, prefix) {
			out = append(out, u)
		}
	}
	return out
}

func checkHeaders(t *testing.T, hd http.Header) {
	t.Helper()
	csp := hd.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'wasm-unsafe-eval'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "default-src 'none'") ||
		!strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP %q", csp)
	}
	if hd.Get("X-Robots-Tag") == "" || !strings.Contains(hd.Get("X-Robots-Tag"), "noindex") {
		t.Errorf("X-Robots-Tag %q", hd.Get("X-Robots-Tag"))
	}
	if hd.Get("Referrer-Policy") != "no-referrer" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers %v", hd)
	}
}

// noInlineScript checks that every script on the page comes from a file, and nothing is styled inline.
func noInlineScript(t *testing.T, body string) {
	t.Helper()
	for _, m := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
		if !strings.Contains(m, `src="/static/`) {
			t.Errorf("inline script: %s", m)
		}
	}
	if strings.Contains(body, " style=") || strings.Contains(body, "<style") || strings.Contains(body, "javascript:") {
		t.Error("the page has inline styles or javascript: links")
	}
	if strings.Contains(body, hostile) {
		t.Error("hostile text reached the page unescaped")
	}
	for _, u := range hrefs(body, "http") {
		if !strings.HasPrefix(u, publicURL) && !projectLink(u) {
			t.Errorf("a link leaves Beacon: %s", u)
		}
	}
}

// projectLink reports whether u is one of the project's own addresses, the only places outside
// Beacon a page of the console links to by itself.
func projectLink(u string) bool {
	return slices.Contains([]string{docsURL(i18n.EN), docsURL(i18n.ZH), version.Repo, issuesURL}, u)
}

func TestAlertPage(t *testing.T) {
	e := newEnv(t, i18n.EN)
	path := link.AlertPath(e.id)
	code, body, hd := e.get(e.link(path), "")
	if code != 200 {
		t.Fatalf("alert page: %d %s", code, body)
	}
	checkHeaders(t, hd)
	noInlineScript(t, body)
	if ct := hd.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
	if hd.Get("Cache-Control") != "private, no-store" {
		t.Errorf("cache control %q", hd.Get("Cache-Control"))
	}
	for _, want := range []string{
		`<html lang="en">`, "git.example.com is down &lt;script&gt;", "Firing", "Critical", "Since 2026-11-02 09:50:00 (10 min)",
		"No answer from 2 locations", "edge-1", "claude-code", "restarting the relay &lt;script&gt;", // the note
		"systemctl restart relay", "false &lt;script&gt;", "exit 1", // the shell's commands, with exit statuses
		"1 sessions on 1 machines, 2 commands", "Timeline", "Alert started", "left a note via MCP", "Replay",
		"Agents reach machines through the SSH channel", "This link is valid until 2026-11-02 11:00:00", `data-brand="beacon"`,
		"No probe results in the window", `<dt>note</dt><dd class="ol-mono ol-break">&lt;script&gt;alert(1)&lt;/script&gt;</dd>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, "codex") || strings.Contains(body, "edge-2") {
		t.Error("another machine's session is on the page")
	}
	replays := hrefs(body, link.SessionPath(sessionID))
	if len(replays) < 2 { // the sessions block and the timeline
		t.Fatalf("replay links: %v", replays)
	}
	u, _ := url.Parse(replays[0])
	if until, ok := e.signer.Verify(http.MethodGet, u.Path, u.Query().Get(link.Param)); !ok || !until.Equal(e.now.Add(time.Hour)) {
		t.Errorf("the replay link %q does not verify, or lasts %v rather than as long as the page's link", replays[0], until)
	}
	if code, body, _ := e.get(replays[0], ""); code != 200 || !strings.Contains(body, sessionID) {
		t.Errorf("following the replay link: %d", code)
	}
}

func TestSessionPage(t *testing.T) {
	e := newEnv(t, i18n.EN)
	path := link.SessionPath(sessionID)
	code, body, hd := e.get(e.link(path), "")
	if code != 200 {
		t.Fatalf("session page: %d %s", code, body)
	}
	checkHeaders(t, hd)
	noInlineScript(t, body)
	for _, want := range []string{
		"claude-code", "edge-1", sessionID, "interactive shell", "SSH", "2 min", "exit 0",
		"systemctl restart relay", "false &lt;script&gt;", "exit 1", "1.2 s", "5 ms",
		`<script src="/static/asciinema-player.min.js?v=`, `<script src="/static/beacon-player.js?v=`,
		`<link rel="stylesheet" href="/static/asciinema-player.css?v=`, `id="player"`, `data-src="/console/sessions/` + sessionID + `/recording?s=`,
		`data-error="The recording could not be loaded"`, "Download the recording",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	recs := hrefs(body, link.RecordingPath(sessionID))
	if len(recs) == 0 {
		t.Fatal("no recording link")
	}
	code, cast, hd := e.get(recs[0], "")
	if code != 200 || hd.Get("Content-Type") != "application/x-asciicast" || !strings.HasPrefix(cast, `{"version":3,`) || !strings.Contains(cast, `"x","0"`) {
		t.Errorf("recording: %d %q %q", code, hd.Get("Content-Type"), cast)
	}
	if code, _, _ := e.get(link.RecordingPath(sessionID), ""); code != 404 {
		t.Errorf("the recording without a signature: %d", code)
	}

	// a session still open, without a transcript: its record alone makes a page, without a player
	open := store.SessionRecord{ID: "20261102-095900-abcdef-1", Agent: "claude-code", Via: "mcp", Machine: "edge-1", Kind: "exec", Command: "journalctl -u relay",
		Started: e.now.Add(-time.Minute), Recording: "2026/11/02/20261102-095900-abcdef-1"}
	_ = e.st.PutSession(open)
	code, body, _ = e.get(e.link(link.SessionPath(open.ID)), "")
	if code != 200 || !strings.Contains(body, "Still open") || !strings.Contains(body, "the recording plays once it ends") || strings.Contains(body, `id="player"`) {
		t.Errorf("open session: %d %s", code, body)
	}
}

func TestAccess(t *testing.T) {
	e := newEnv(t, i18n.EN)
	alert, session := link.AlertPath(e.id), link.SessionPath(sessionID)
	cases := []struct {
		name  string
		path  string
		token string
		code  int
	}{
		{"alert without a signature", alert, "", 404},
		{"alert with a signature for another path", alert + "?" + e.signer.Query(link.AlertPath("0cdfbada62de")), "", 404},
		{"alert with a tampered signature", e.link(alert) + "x", "", 404},
		{"unknown alert with a good signature", e.link(link.AlertPath("0cdfbada62de")), "", 404},
		{"unknown session with a good signature", e.link(link.SessionPath("20261102-000000-abcdef-1")), "", 404},
		{"session with a token lacking the scope", session, e.token("reader", "status:read"), 404},
		{"alert with a token lacking the scope", alert, e.token("ssh-only", "machines:ssh"), 404},
		{"a wrong token", alert, "lbt_wrong", 404},
		{"alert with status:read", alert, e.token("status", "status:read"), 200},
		{"session with sessions:read", session, e.token("sessions", "sessions:read"), 200},
		{"session with machines:ssh", session, e.token("ssh", "machines:ssh"), 200},
		{"recording with sessions:read", link.RecordingPath(sessionID), e.token("rec", "sessions:read"), 200},
		{"static needs nothing", "/static/beacon.css", "", 200},
		{"nothing else under static", "/static/NOTICE", "", 404},
	}
	for _, c := range cases {
		code, body, hd := e.get(c.path, c.token)
		if code != c.code {
			t.Errorf("%s: %d, want %d", c.name, code, c.code)
		}
		if code == 404 && (strings.Contains(body, "edge-1") || strings.Contains(body, "git.example.com") || hd.Get("WWW-Authenticate") != "") {
			t.Errorf("%s: a 404 that says something: %s %v", c.name, body, hd)
		}
		checkHeaders(t, hd)
	}
	// a token-opened session page links to its recording for an hour, and says it was a token
	code, body, _ := e.get(session, e.token("sessions2", "sessions:read"))
	if code != 200 || !strings.Contains(body, "Opened with API access") {
		t.Fatalf("token page: %d", code)
	}
	recs := hrefs(body, link.RecordingPath(sessionID))
	u, _ := url.Parse(recs[0])
	if until, ok := e.signer.Verify(http.MethodGet, u.Path, u.Query().Get(link.Param)); !ok || !until.Equal(e.now.Add(tokenTTL)) {
		t.Errorf("the recording link of a token-opened page lasts %v", until)
	}
	// the link expires
	signed := e.link(alert)
	e.now = e.now.Add(time.Hour)
	if code, _, _ := e.get(signed, ""); code != 404 {
		t.Errorf("an expired link opens the page: %d", code)
	}
	// without the SSH channel, session pages do not exist
	e.srv.SSH = nil
	if code, _, _ := e.get(e.link(session), ""); code != 404 {
		t.Errorf("a session page without the channel: %d", code)
	}
}

func TestStatic(t *testing.T) {
	e := newEnv(t, i18n.EN)
	for name, ct := range staticFiles {
		code, body, hd := e.get("/static/"+name, "")
		if code != 200 || hd.Get("Content-Type") != ct || hd.Get("ETag") == "" || len(body) == 0 || !strings.Contains(hd.Get("Cache-Control"), "max-age") {
			t.Errorf("%s: %d %q %q", name, code, hd.Get("Content-Type"), hd.Get("ETag"))
		}
		req := httptest.NewRequest(http.MethodGet, "/static/"+name, nil)
		req.Header.Set("If-None-Match", hd.Get("ETag"))
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("%s: If-None-Match answers %d", name, rec.Code)
		}
	}
	_, js, _ := e.get("/static/asciinema-player.min.js", "")
	if !strings.Contains(js, "asciicast v3") {
		t.Error("the player bundle does not mention asciicast v3")
	}
	if code, body, _ := e.get("/static/../web.go", ""); code == 200 || strings.Contains(body, "package web") {
		t.Errorf("a path outside static: %d", code) // the mux cleans the path and redirects; the cleaned path is 404
	}
}

func TestChinesePage(t *testing.T) {
	e := newEnv(t, i18n.ZH)
	code, body, _ := e.get(e.link(link.AlertPath(e.id)), "")
	if code != 200 || !strings.Contains(body, `<html lang="zh-CN">`) || !strings.Contains(body, i18n.T(i18n.ZH, "Timeline")) ||
		!strings.Contains(body, i18n.T(i18n.ZH, "Firing")) || !strings.Contains(body, i18n.T(i18n.ZH, "Replay")) {
		t.Errorf("Chinese page: %d %s", code, body)
	}
	code, body, _ = e.get(e.link(link.SessionPath(sessionID)), "")
	if code != 200 || !strings.Contains(body, i18n.T(i18n.ZH, "interactive shell")) || !strings.Contains(body, i18n.T(i18n.ZH, "Download the recording")) {
		t.Errorf("Chinese session page: %d", code)
	}
}

// findBrowser returns the browser BEACON_BROWSER names, or, for "auto", a Chrome, Chromium or Edge found
// where they are usually installed; "" when there is none.
func findBrowser() string {
	b := os.Getenv("BEACON_BROWSER")
	if b != "auto" {
		return b
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "msedge", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`, `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, `C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// TestBrowser opens the session page in a headless browser (BEACON_BROWSER names a Chrome or Edge
// binary, or is "auto") and checks that the player rendered the recording under the pages'
// Content-Security-Policy: the terminal's text appears in the DOM only when the WebAssembly ran and the
// recording was fetched.
func TestBrowser(t *testing.T) {
	browser := findBrowser()
	if browser == "" {
		t.Skip("set BEACON_BROWSER to a Chrome or Edge binary, or to auto, to run the player under the CSP")
	}
	e := newEnv(t, i18n.EN)
	srv := httptest.NewServer(e.mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--virtual-time-budget=8000",
		"--enable-logging=stderr", "--user-data-dir="+t.TempDir(), "--dump-dom", srv.URL+e.link(link.SessionPath(sessionID)))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", browser, err, stderr.String())
	}
	dom := string(out)
	if strings.TrimSpace(dom) == "" { // a browser always dumps at least <html>: it could not run here (a sandbox, say)
		t.Skipf("%s produced no DOM here\n%s", browser, stderr.String())
	}
	if !strings.Contains(dom, "ap-player") || !strings.Contains(dom, "relay restarted") {
		t.Errorf("the player did not render the recording (%s):\n%s\n%s", browser, dom, stderr.String())
	}
	if strings.Contains(stderr.String(), "Content Security Policy") || strings.Contains(stderr.String(), "Refused to") {
		t.Errorf("the browser reported a policy violation:\n%s", stderr.String())
	}
}
