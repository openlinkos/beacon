package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// browser is a person at the console: the cookies the console set, and the CSRF secret of the last
// page that had one.
type browser struct {
	e       *env
	cookies map[string]*http.Cookie
	csrf    string
	headers map[string]string
}

func (e *env) browser() *browser {
	return &browser{e: e, cookies: map[string]*http.Cookie{}, headers: map[string]string{}}
}

type answer struct {
	code   int
	body   string
	header http.Header
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
var formRe = regexp.MustCompile(`name="form" value="([^"]+)"`)

func (b *browser) do(method, path string, form url.Values) answer {
	b.e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	rec := httptest.NewRecorder()
	b.e.mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	out, _ := io.ReadAll(res.Body)
	a := answer{code: res.StatusCode, body: string(out), header: res.Header}
	if m := csrfRe.FindStringSubmatch(a.body); m != nil {
		b.csrf = m[1]
	}
	return a
}

func (b *browser) get(path string) answer { return b.do(http.MethodGet, path, nil) }

// post sends a form with the page's CSRF secret.
func (b *browser) post(path string, form url.Values) answer {
	if form == nil {
		form = url.Values{}
	}
	if form.Get("csrf") == "" {
		form.Set("csrf", b.csrf)
	}
	return b.do(http.MethodPost, path, form)
}

// signIn pastes secret into the sign-in form and sends it.
func (b *browser) signIn(secret, next string) answer {
	b.e.t.Helper()
	page := b.get("/sign-in?next=" + url.QueryEscape(next))
	m := formRe.FindStringSubmatch(page.body)
	if page.code != 200 || m == nil {
		b.e.t.Fatalf("sign-in page: %d %s", page.code, page.body)
	}
	return b.do(http.MethodPost, "/sign-in", url.Values{"token": {secret}, "next": {next}, "form": {m[1]}})
}

// signedIn is a browser signed in with a token of these scopes.
func (e *env) signedIn(name string, scopes ...string) *browser {
	e.t.Helper()
	b := e.browser()
	if a := b.signIn(e.token(name, scopes...), "/"); a.code != http.StatusSeeOther {
		e.t.Fatalf("signing in as %s: %d %s", name, a.code, a.body)
	}
	b.get("/") // for the CSRF secret
	return b
}

func TestSignIn(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.browser()

	// signed out, the console sends people to sign in, and back
	if a := b.get("/alerts?state=firing"); a.code != http.StatusSeeOther || a.header.Get("Location") != "/sign-in?next="+url.QueryEscape("/alerts?state=firing") {
		t.Fatalf("signed out: %d %q", a.code, a.header.Get("Location"))
	}
	page := b.get("/sign-in")
	checkHeaders(t, page.header)
	if page.code != 200 || !strings.Contains(page.body, `type="password"`) || !strings.Contains(page.body, `action="/sign-in"`) {
		t.Fatalf("sign-in page: %d", page.code)
	}
	fc := b.cookies[signInCookie]
	if fc == nil || !fc.HttpOnly || !fc.Secure || fc.SameSite != http.SameSiteStrictMode || fc.Path != "/" {
		t.Errorf("the sign-in form's cookie: %+v", fc)
	}

	// a wrong token: the form again, no session, the failure counted
	if a := b.signIn("lbt_wrong", "/"); a.code != http.StatusUnauthorized || !strings.Contains(a.body, "The token is unknown or has expired.") || b.cookies[sessionCookie] != nil {
		t.Errorf("a wrong token: %d %v", a.code, b.cookies)
	}
	// a token without status:read cannot sign in
	if a := b.signIn(e.token("probe-only", "machines:ssh"), "/"); a.code != http.StatusForbidden || !strings.Contains(a.body, "lacks status:read") || b.cookies[sessionCookie] != nil {
		t.Errorf("a token without status:read: %d", a.code)
	}
	// a form posted without its own secret (from another site, say) does nothing
	secret := e.token("alice", "status:read", "alerts:write")
	if a := b.do(http.MethodPost, "/sign-in", url.Values{"token": {secret}, "next": {"/"}}); a.code != http.StatusForbidden || b.cookies[sessionCookie] != nil {
		t.Errorf("a sign-in without the form's secret: %d", a.code)
	}

	// the right token: a session in an HttpOnly, Secure, SameSite=Strict cookie, for 12 hours at most
	a := b.signIn(secret, "/alerts/"+e.id)
	if a.code != http.StatusSeeOther || a.header.Get("Location") != "/alerts/"+e.id {
		t.Fatalf("signing in: %d %q %s", a.code, a.header.Get("Location"), a.body)
	}
	c := b.cookies[sessionCookie]
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" ||
		!strings.HasPrefix(c.Name, "__Host-") || c.MaxAge <= 0 || c.MaxAge > int(sessionTTL.Seconds()) {
		t.Fatalf("the session cookie: %+v", c)
	}
	if strings.Contains(c.Value, secret) || strings.Contains(c.Value, "lbt_") {
		t.Error("the cookie holds the token")
	}
	if b.cookies[signInCookie] != nil {
		t.Error("the sign-in form's cookie outlives the sign-in")
	}
	home := b.get("/")
	if home.code != 200 || !strings.Contains(home.body, "alice") || !strings.Contains(home.body, `action="/sign-out"`) {
		t.Fatalf("signed in: %d", home.code)
	}
	if e.st.AuditLog(1)[0].Action != "console_sign_in" {
		t.Error("the sign-in is not in the audit log")
	}
	// signed in, the sign-in page sends people on
	if a := b.get("/sign-in?next=/tokens"); a.code != http.StatusSeeOther || a.header.Get("Location") != "/tokens" {
		t.Errorf("the sign-in page when signed in: %d %q", a.code, a.header.Get("Location"))
	}
	// next never leads off the site
	for _, next := range []string{"//evil.example.com/x", "https://evil.example.com", "/\\evil.example.com", "javascript:alert(1)", "/\t/evil.example.com", "/ /evil.example.com"} {
		if a := b.get("/sign-in?next=" + url.QueryEscape(next)); a.header.Get("Location") != "/" {
			t.Errorf("next %q leads to %q", next, a.header.Get("Location"))
		}
	}

	// what the sign-in page reflects is escaped
	if a := e.browser().get("/sign-in?next=" + url.QueryEscape(`/x"><script>alert(1)</script>`)); a.code != 200 || strings.Contains(a.body, hostile) ||
		len(h1Re.FindAllString(a.body, -1)) != 1 || strings.Contains(a.body, " style=") || strings.Contains(a.body, "<script") {
		t.Errorf("the sign-in page: %d", a.code)
	}

	// signing out needs the form's CSRF secret, then ends the session
	if a := b.post("/sign-out", url.Values{"csrf": {"wrong"}}); a.code != http.StatusForbidden {
		t.Errorf("signing out without the CSRF secret: %d", a.code)
	}
	old := b.cookies[sessionCookie].Value
	if a := b.post("/sign-out", nil); a.code != http.StatusSeeOther || a.header.Get("Location") != "/sign-in" || b.cookies[sessionCookie] != nil {
		t.Errorf("signing out: %d %v", a.code, b.cookies)
	}
	b.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: old}
	if a := b.get("/"); a.code != http.StatusSeeOther {
		t.Errorf("the session outlives signing out: %d", a.code)
	}
}

func TestSignInRateLimit(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.browser()
	secret := e.token("alice", "status:read")
	for range 20 {
		b.signIn("lbt_wrong", "/")
	}
	a := b.signIn(secret, "/")
	if a.code != http.StatusTooManyRequests || b.cookies[sessionCookie] != nil || !strings.Contains(a.body, "Too many failed sign-ins") {
		t.Errorf("after 20 failures: %d", a.code)
	}
	// the API's limit and the console's are one
	req := httptest.NewRequest(http.MethodGet, "/alerts/"+e.id, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("a page by token while blocked: %d", rec.Code)
	}
}

func TestRevokedTokenEndsSession(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.signedIn("bob", "status:read")
	if a := b.get("/"); a.code != 200 {
		t.Fatalf("signed in: %d", a.code)
	}
	if err := e.st.RevokeToken("bob"); err != nil {
		t.Fatal(err)
	}
	if a := b.get("/"); a.code != http.StatusSeeOther {
		t.Errorf("a revoked token's session: %d", a.code)
	}
	// nor does a new token of the same name take the old session over
	b = e.signedIn("carol", "status:read")
	_ = e.st.RevokeToken("carol")
	e.token("carol", "status:read")
	if a := b.get("/"); a.code != http.StatusSeeOther {
		t.Errorf("a replaced token's session: %d", a.code)
	}
}

// writes are every form the console posts, with what it sends.
func (e *env) writes() map[string]url.Values {
	return map[string]url.Values{
		"/alerts/" + e.id + "/ack":     {"note": {"on it"}},
		"/alerts/" + e.id + "/silence": {"duration": {"4h"}, "comment": {"maintenance"}},
		"/alerts/" + e.id + "/notes":   {"text": {"restarted the relay"}},
		"/machines/pause":              {},
		"/machines/resume":             {},
		"/tokens/agent-1/revoke":       {},
		"/config/history/1/revert":     {"note": {"undo"}},
		"/sign-out":                    {},
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t, i18n.EN)
	e.token("agent-1", "machines:ssh")
	b := e.signedIn("root", "status:read", "alerts:write", "machines:write", "config:write", "sessions:read")
	audit := len(e.st.AuditLog(10000))
	for path, form := range e.writes() {
		for name, try := range map[string]func() answer{
			"no secret":    func() answer { f := clone(form); return b.do(http.MethodPost, path, f) },
			"wrong secret": func() answer { f := clone(form); f.Set("csrf", b.csrf+"x"); return b.do(http.MethodPost, path, f) },
			"cross-site": func() answer {
				b.headers["Sec-Fetch-Site"] = "cross-site"
				defer delete(b.headers, "Sec-Fetch-Site")
				return b.post(path, clone(form))
			},
		} {
			if a := try(); a.code != http.StatusForbidden {
				t.Errorf("%s, %s: %d", path, name, a.code)
			}
		}
		// signed out, a form does nothing either
		if a := e.browser().do(http.MethodPost, path, clone(form)); a.code != http.StatusSeeOther || !strings.HasPrefix(a.header.Get("Location"), "/sign-in") {
			t.Errorf("%s signed out: %d", path, a.code)
		}
	}
	if n := len(e.st.AuditLog(10000)); n != audit {
		t.Errorf("forms without their secret changed something: %d audit entries, was %d", n, audit)
	}
	if e.srv.SSH.Paused() {
		t.Error("the channel was paused")
	}
	if g, _ := e.st.Group(e.id); g.Ack != nil || g.Silence != nil || len(g.Notes) != 1 {
		t.Error("the alert was acted on")
	}
}

func clone(v url.Values) url.Values {
	out := url.Values{}
	for k, x := range v {
		out[k] = slices.Clone(x)
	}
	return out
}

func TestScopes(t *testing.T) {
	e := newEnv(t, i18n.EN)
	e.token("agent-1", "machines:ssh")
	_, _, _ = e.st.CreateSubjectToken("probe-edge-1", "edge-1", []string{"probe:write"}, time.Hour, "test")
	reader := e.signedIn("reader", "status:read")
	for path, form := range e.writes() {
		if path == "/sign-out" {
			continue
		}
		if a := reader.post(path, clone(form)); a.code != http.StatusForbidden || !strings.Contains(a.body, "This needs a token with") {
			t.Errorf("%s without the scope: %d", path, a.code)
		}
	}
	if g, _ := e.st.Group(e.id); g.Ack != nil || e.srv.SSH.Paused() {
		t.Fatal("a token without the scope acted")
	}
	// a session's page needs sessions:read or machines:ssh: signed in without either, the page says so
	if a := reader.get(link.SessionPath(sessionID)); a.code != http.StatusForbidden || !strings.Contains(a.body, "sessions:read") {
		t.Errorf("a session's page without sessions:read: %d", a.code)
	}
	// the reader sees no forms
	if a := reader.get(link.AlertPath(e.id)); strings.Contains(a.body, "/ack\"") || strings.Contains(a.body, "/silence\"") {
		t.Error("the alert's page offers a reader its forms")
	}
	if a := reader.get("/machines"); strings.Contains(a.body, `action="/machines/pause"`) {
		t.Error("the machines' page offers a reader the pause switch")
	}
	if a := reader.get("/tokens"); strings.Contains(a.body, "/revoke") {
		t.Error("the tokens' page offers a reader to revoke")
	}

	// with alerts:write: acknowledge, silence, add a note, each audited and echoed as the console's
	w := e.signedIn("writer", "status:read", "alerts:write")
	page := w.get(link.AlertPath(e.id))
	for _, form := range []string{`action="/alerts/` + e.id + `/ack"`, `action="/alerts/` + e.id + `/silence"`, `action="/alerts/` + e.id + `/notes"`, `value="24h"`} {
		if !strings.Contains(page.body, form) {
			t.Errorf("the alert's page lacks %s", form)
		}
	}
	if a := w.post("/alerts/"+e.id+"/notes", url.Values{"text": {"restarted the relay " + hostile}}); a.code != http.StatusSeeOther || a.header.Get("Location") != link.AlertPath(e.id)+"?done=note" {
		t.Errorf("a note: %d %q", a.code, a.header.Get("Location"))
	}
	if a := w.post("/alerts/"+e.id+"/notes", url.Values{"text": {"  "}}); a.header.Get("Location") != link.AlertPath(e.id)+"?error=bad_input" {
		t.Errorf("an empty note: %q", a.header.Get("Location"))
	}
	if a := w.post("/alerts/"+e.id+"/ack", url.Values{"note": {"on it"}}); a.code != http.StatusSeeOther {
		t.Errorf("acknowledging: %d", a.code)
	}
	if a := w.post("/alerts/"+e.id+"/silence", url.Values{"duration": {"7d"}, "comment": {"x"}}); a.header.Get("Location") != link.AlertPath(e.id)+"?error=bad_input" {
		t.Errorf("a silence the console does not offer: %q", a.header.Get("Location"))
	}
	if a := w.post("/alerts/"+e.id+"/silence", url.Values{"duration": {"4h"}, "comment": {"maintenance"}}); a.code != http.StatusSeeOther {
		t.Errorf("silencing: %d", a.code)
	}
	g, _ := e.st.Group(e.id)
	if g.Ack == nil || g.Ack.By != "writer" || g.Ack.Via != "console" || g.Silence == nil || len(g.Notes) != 2 || g.Notes[1].Via != "console" {
		t.Fatalf("the alert after the forms: ack %+v silence %+v notes %+v", g.Ack, g.Silence, g.Notes)
	}
	for _, action := range []string{"add_note", "ack", "silence"} {
		if !audited(e.st, "writer", action) {
			t.Errorf("%s is not audited as the console's", action)
		}
	}
	if !e.bot.told("writer", "via the console") {
		t.Errorf("the chat was not told: %v", e.bot.sent)
	}
	done := w.get(link.AlertPath(e.id) + "?done=ack")
	if !strings.Contains(done.body, "Alert acknowledged") || !strings.Contains(done.body, "ol-toast") {
		t.Error("no confirmation after acknowledging")
	}
	if bad := w.get(link.AlertPath(e.id) + "?error=bad_input"); !strings.Contains(bad.body, "ol-alert-danger") {
		t.Error("no problem shown after a form that failed")
	}

	// with machines:write: the pause switch, and revoking an agent's token, not a probe's
	m := e.signedIn("operator", "status:read", "machines:write")
	if page := m.get("/machines"); !strings.Contains(page.body, `action="/machines/pause"`) {
		t.Error("the machines' page lacks the pause switch")
	}
	if a := m.post("/machines/pause", nil); a.code != http.StatusSeeOther || !e.srv.SSH.Paused() || !audited(e.st, "operator", "ssh_pause") {
		t.Errorf("pausing: %d %v", a.code, e.srv.SSH.Paused())
	}
	if page := m.get("/machines?done=paused"); !strings.Contains(page.body, `action="/machines/resume"`) || !strings.Contains(page.body, "Paused") {
		t.Error("the paused channel does not offer to resume")
	}
	if a := m.post("/machines/resume", nil); a.code != http.StatusSeeOther || e.srv.SSH.Paused() || !audited(e.st, "operator", "ssh_resume") {
		t.Errorf("resuming: %d", a.code)
	}
	tokens := m.get("/tokens")
	if !strings.Contains(tokens.body, `action="/tokens/agent-1/revoke"`) || strings.Contains(tokens.body, `action="/tokens/probe-edge-1/revoke"`) {
		t.Error("the tokens' page offers the wrong tokens to revoke")
	}
	if a := m.post("/tokens/probe-edge-1/revoke", nil); a.header.Get("Location") != "/tokens?error=not_revocable" {
		t.Errorf("revoking a probe's token: %q", a.header.Get("Location"))
	}
	if a := m.post("/tokens/agent-1/revoke", nil); a.code != http.StatusSeeOther || a.header.Get("Location") != "/tokens?done=revoked" {
		t.Errorf("revoking: %d %q", a.code, a.header.Get("Location"))
	}
	names := map[string]bool{}
	for _, tk := range e.st.Tokens() {
		names[tk.Name] = true
	}
	if names["agent-1"] || !names["probe-edge-1"] || !audited(e.st, "operator", "revoke_token") || !e.bot.told("operator", "agent-1", "via the console") {
		t.Errorf("after revoking: %v", names)
	}

	// with config:write: revert a version
	c := e.signedIn("configurer", "status:read", "config:write")
	if page := c.get("/config/history/2"); !strings.Contains(page.body, `action="/config/history/2/revert"`) {
		t.Error("the version's page lacks the revert form")
	}
	before := e.cdb.Version()
	if a := c.post("/config/history/2/revert", url.Values{"note": {"not yet"}}); a.code != http.StatusSeeOther || e.cdb.Version() != before+1 {
		t.Errorf("reverting: %d %q, version %d", a.code, a.header.Get("Location"), e.cdb.Version())
	}
	if h, _ := e.cdb.History(configHistoryFilter(1)); h[0].Via != "console" || h[0].Actor != "configurer" || h[0].Reverts != 2 {
		t.Errorf("the revert: %+v", h[0])
	}
	if a := c.post("/config/history/2/revert", nil); a.header.Get("Location") != "/config/history/2?error=conflict" {
		t.Errorf("reverting twice: %q", a.header.Get("Location"))
	}
}

func audited(st *store.Store, actor, action string) bool {
	for _, a := range st.AuditLog(10000) {
		if a.Actor == actor && a.Action == action && a.Via == "console" {
			return true
		}
	}
	return false
}

// consolePages are every page of the console, with what each must show of the test's data.
func (e *env) consolePages() map[string][]string {
	return map[string][]string{
		"/":                         {"Firing alerts", "Probe locations", "1 of 2", "Agents woken today", "Sessions today", "v0.3.0-test", "git.example.com is down"},
		"/alerts":                   {"git.example.com is down &lt;script&gt;", "Firing", "Critical", "git-example-com", `ol-facet`},
		"/alerts?severity=warning":  {"Nothing matches."},
		link.AlertPath(e.id):        {"git.example.com is down &lt;script&gt;", "restarting the relay &lt;script&gt;", "Timeline", "relay 1.2.3 &lt;script&gt;"},
		"/machines":                 {"edge-1", "edge-2", "192.0.2.10:22", "Behind NAT, not connected", "Running"},
		"/machines/edge-1":          {"edge-1", "beacon", "systemctl restart relay", "false &lt;script&gt;", "the relay &lt;script&gt;"},
		"/sessions":                 {"claude-code", "codex", "uptime", "interactive shell", "2026-11-02"},
		"/sessions?agent=codex":     {"uptime", "Agent: codex"},
		link.SessionPath(sessionID): {"claude-code on edge-1", "systemctl restart relay", `id="player"`},
		"/tokens":                   {"root", "status:read", "All machines"},
		"/config":                   {"edge-1", "Europe - Frankfurt", "Asia - Shanghai", "alice", "new location &lt;script&gt;"},
		"/config/history/1":         {"location edge-1", "Europe - Frankfurt", `data-op="add"`},
		"/agents":                   {"oncall", "hooks.example.com", "202", "Done"},
		"/audit":                    {"add_note", "claude-code", "console_sign_in"},
		"/changes":                  {"relay 1.2.3 &lt;script&gt;", "flux", "deploy"},
	}
}

func TestConsolePages(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZH} {
		e := newEnv(t, lang)
		e.token("agent-1", "machines:ssh")
		b := e.signedIn("root", "status:read", "alerts:write", "machines:write", "config:write", "sessions:read")
		for path, wants := range e.consolePages() {
			a := b.get(path)
			if a.code != 200 {
				t.Errorf("%s %s: %d %s", lang, path, a.code, a.body)
				continue
			}
			checkHeaders(t, a.header)
			checkPage(t, path, a.body)
			if lang == i18n.ZH {
				if !strings.Contains(a.body, `<html lang="zh-CN">`) || !strings.Contains(a.body, i18n.T(i18n.ZH, "Console")) || !strings.Contains(a.body, i18n.T(i18n.ZH, "Sign out")) {
					t.Errorf("%s: not in Chinese", path)
				}
				continue
			}
			for _, want := range wants {
				if !strings.Contains(a.body, want) {
					t.Errorf("%s lacks %q", path, want)
				}
			}
		}
		// the hook's secret never shows
		if a := b.get("/agents"); strings.Contains(a.body, "hook-secret") {
			t.Error("the agents' page shows a hook's URL with its query")
		}
		// a page that is not there, signed in: said so in the shell
		if a := b.get("/machines/nowhere"); a.code != http.StatusNotFound || !strings.Contains(a.body, "ol-app-workbench") {
			t.Errorf("a machine that does not exist: %d", a.code)
		}
		if a := b.get("/config/history/99"); a.code != http.StatusNotFound {
			t.Errorf("a version that does not exist: %d", a.code)
		}
	}
}

var h1Re = regexp.MustCompile(`<h1[\s>]`)

// checkPage checks what every page of the console has: one h1, a breadcrumb, the design system's
// workbench, no inline style or script, nothing hostile unescaped.
func checkPage(t *testing.T, path, body string) {
	t.Helper()
	if n := len(h1Re.FindAllString(body, -1)); n != 1 {
		t.Errorf("%s has %d h1", path, n)
	}
	if !strings.Contains(body, `<ol class="ol-breadcrumb">`) {
		t.Errorf("%s has no breadcrumb", path)
	}
	for _, want := range []string{`class="ol-app ol-app-workbench"`, `href="/static/ol/ui.css?v=`, `data-brand="beacon"`, `class="ol-sidebar" id="nav" popover`, `ol-nav-open`, `<symbol id="i-`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s lacks %s", path, want)
		}
	}
	noInlineScript(t, body)
}

func TestShellsDifferOnlyByCSS(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.signedIn("root", "status:read", "sessions:read")
	clients := []map[string]string{
		{"User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Mobile/15E148", "Sec-CH-Viewport-Width": "360", "Viewport-Width": "360", "Sec-CH-UA-Mobile": "?1"},
		{"User-Agent": "Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X)", "Sec-CH-Viewport-Width": "860", "Viewport-Width": "860"},
		{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/140.0", "Sec-CH-Viewport-Width": "1200", "Viewport-Width": "1200", "Sec-CH-UA-Mobile": "?0"},
	}
	for path := range e.consolePages() {
		var first string
		for i, h := range clients {
			b.headers = h
			a := b.get(path)
			if i == 0 {
				first = a.body
				if a.header.Get("Vary") != "" {
					t.Errorf("%s varies by %q", path, a.header.Get("Vary"))
				}
				continue
			}
			if a.body != first {
				t.Errorf("%s differs between a %s and a %s wide screen", path, clients[0]["Viewport-Width"], h["Viewport-Width"])
			}
		}
	}
	// the widths are the design system's: its stylesheet holds the three tiers
	css, err := static.ReadFile("static/ol/ui.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"860px", "1200px", ".ol-table-stack", ".ol-app-workbench"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("ui.css lacks %s", want)
		}
	}
}

func TestOverviewFigures(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.signedIn("root", "status:read")
	v := &viewer{Token: tokenNamed(e.st, "root")}
	f := e.srv.figures(v)
	firing := map[string]int{}
	for _, g := range e.st.Groups() {
		if g.Firing() {
			firing[g.Severity]++
		}
	}
	today := 0
	for _, r := range e.st.Sessions(1000, nil) {
		if !r.Started.Before(e.srv.today()) {
			today++
		}
	}
	want := overviewFigures{Firing: firing, FiringTotal: 1, Expected: 2, Reporting: 1, ReportingKnown: true, Machines: len(e.st.Machines()), Critical: 1,
		Connectors: 1, Connected: 0, Woken: 1, WokenOK: 1, SessionsToday: today, ChannelOn: true, ConfigVersion: e.cdb.Version()}
	if f.FiringTotal != want.FiringTotal || f.Firing["critical"] != firing["critical"] || f.Expected != want.Expected || f.Reporting != want.Reporting ||
		f.Machines != want.Machines || f.Critical != want.Critical || f.Connectors != want.Connectors || f.Connected != want.Connected ||
		f.Woken != want.Woken || f.WokenOK != want.WokenOK || f.SessionsToday != want.SessionsToday || f.ConfigVersion != want.ConfigVersion || today != 2 {
		t.Errorf("figures %+v, want %+v", f, want)
	}
	page := b.get("/").body
	for _, want := range []string{
		`href="/alerts?state=firing"`, `<span class="ol-stat-value">1</span>`, "1 critical, 0 warning, 0 info",
		`href="/config#locations"`, "1 of 2", `href="/machines"`, "1 critical, 0 of 1 behind NAT connected",
		`href="/agents"`, "1 woken, 0 failed, 0 waiting", `href="/sessions?day=2026-11-02"`, `<span class="ol-stat-value">2</span>`,
		"Running", ">" + strconv.FormatInt(e.cdb.Version(), 10) + ", by alice via API",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
	// pausing shows on the overview at once
	_ = e.srv.SSH.SetPaused(context.Background(), sshActor("root"), true)
	if page := b.get("/").body; !strings.Contains(page, "Paused") || !strings.Contains(page, "ol-status-danger") {
		t.Error("the overview does not say the channel is paused")
	}
}

func sshActor(name string) sshgate.Actor { return sshgate.Actor{Name: name, Via: "console"} }

func tokenNamed(st *store.Store, name string) store.Token {
	for _, t := range st.Tokens() {
		if t.Name == name {
			return t
		}
	}
	return store.Token{}
}

func TestViewOnlyLinks(t *testing.T) {
	e := newEnv(t, i18n.EN)
	for _, path := range []string{link.AlertPath(e.id), link.SessionPath(sessionID)} {
		a := e.browser().get(e.link(path))
		if a.code != 200 {
			t.Fatalf("%s by link: %d", path, a.code)
		}
		checkPage(t, path, a.body)
		if strings.Contains(a.body, `method="post"`) || strings.Contains(a.body, `href="/tokens"`) || !strings.Contains(a.body, `href="/sign-in"`) {
			t.Errorf("%s by link is not view-only", path)
		}
	}
	// signed in, the same link shows the console's page with its forms
	b := e.signedIn("writer", "status:read", "alerts:write")
	if a := b.get(e.link(link.AlertPath(e.id))); !strings.Contains(a.body, `/ack"`) || !strings.Contains(a.body, `href="/tokens"`) {
		t.Error("signed in, the link does not open the console's page")
	}
}

func TestFilter(t *testing.T) {
	e := newEnv(t, i18n.EN)
	type item struct{ kind, day string }
	var items []item
	for i := range 120 {
		k := "a"
		if i%3 == 0 {
			k = "b"
		}
		items = append(items, item{k, "d" + strconv.Itoa(i%2)})
	}
	facets := []facet[item]{
		{Param: "kind", Title: "Kind", Value: func(x item) string { return x.kind }},
		{Param: "day", Title: "Day", Value: func(x item) string { return x.day }},
	}
	q := url.Values{"kind": {"b"}}
	kept, l := filter(e.srv, "/x", q, items, facets, nil)
	if len(kept) != 40 || l.Total != "40" || l.On != 1 || len(l.Tags) != 1 || l.Tags[0].Href != "/x" {
		t.Fatalf("kept %d, list %+v", len(kept), l)
	}
	// a facet counts under the other filters, not its own
	kinds, days := l.Facets[0], l.Facets[1]
	if kinds.Values[0].Label != "a" || kinds.Values[0].Count != "80" || !kinds.Values[1].On || kinds.Values[1].Count != "40" || kinds.Values[1].Href != "/x" {
		t.Errorf("kinds %+v", kinds)
	}
	if days.Values[0].Count != "20" || days.Values[0].Href != "/x?day=d0&kind=b" {
		t.Errorf("days %+v", days)
	}
	// 50 to a page
	kept, l = filter(e.srv, "/x", url.Values{"page": {"3"}}, items, facets, nil)
	if len(kept) != 20 || l.Pager.Summary != "101 to 120 of 120" || l.Pager.Next != "" || l.Pager.Prev != "/x?page=2" {
		t.Errorf("page 3: %d %+v", len(kept), l.Pager)
	}
	if _, l = filter(e.srv, "/x", url.Values{"page": {"2"}}, items, facets, nil); l.Pager.Prev != "/x" || l.Pager.Next != "/x?page=3" {
		t.Errorf("page 2: %+v", l.Pager)
	}
	if kept, _ = filter(e.srv, "/x", url.Values{"page": {"99"}}, items[:3], facets, nil); len(kept) != 3 {
		t.Errorf("a page past the end: %d", len(kept))
	}
}

// TestTimelineKinds renders the timeline's kinds people read in words, the commands of a shell and a
// heartbeat's last success among them.
func TestTimelineKinds(t *testing.T) {
	e := newEnv(t, i18n.EN)
	one := 1
	tl := bridge.Timeline{Events: []bridge.Event{
		{At: e.now, Kind: "command", By: "claude-code", Machine: "edge-1", Text: "systemctl restart relay", ID: sessionID, Exit: &one},
		{At: e.now, Kind: "heartbeat", ID: "backup", Text: "the nightly backup"},
	}}
	rows := e.srv.events(tl, access{Until: e.now.Add(time.Hour)}, e.srv.labels())
	if rows[0].Text != "claude-code ran a command on edge-1" || rows[0].Detail != "systemctl restart relay (exit 1)" || !strings.HasPrefix(rows[0].Href, link.SessionPath(sessionID)+"?s=") {
		t.Errorf("a command: %+v", rows[0])
	}
	if rows[1].Text != "Heartbeat backup last succeeded" || rows[1].Detail != "the nightly backup" {
		t.Errorf("a heartbeat: %+v", rows[1])
	}
}

func TestDiff(t *testing.T) {
	got := diff([]string{"{", `  "a": 1,`, `  "b": 2`, "}"}, []string{"{", `  "a": 1,`, `  "b": 3`, "}"})
	var ops []string
	for _, l := range got {
		ops = append(ops, l.Op)
	}
	if strings.Join(ops, "") != "  -+ " {
		t.Errorf("diff %v", got)
	}
	if got := diff(nil, []string{"x"}); len(got) != 1 || got[0].Op != "+" {
		t.Errorf("an added item: %v", got)
	}
}

func TestIcons(t *testing.T) {
	for _, name := range iconNames {
		if !strings.Contains(string(sprite), `<symbol id="i-`+name+`"`) {
			t.Errorf("the sprite lacks %s", name)
		}
	}
	if _, err := icon("no-such-icon", ""); err == nil {
		t.Error("an unknown icon draws")
	}
	if strings.Contains(string(sprite), "<script") || strings.Contains(string(sprite), "style=") {
		t.Error("the sprite holds more than shapes")
	}
}
