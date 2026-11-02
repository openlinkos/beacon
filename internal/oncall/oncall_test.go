package oncall

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testSecret = []byte("correct-horse-battery-staple-24")

// syncBuf is a log sink the workers and the test may share.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// newServer starts a harness in a temporary log directory with a clock the test controls.
func newServer(t *testing.T, o Options) (*Server, *syncBuf) {
	t.Helper()
	logs := &syncBuf{}
	o.Log = slog.New(slog.NewTextHandler(logs, nil))
	if o.Secret == nil {
		o.Secret = testSecret
	}
	if o.Run == "" {
		o.Run = "true"
	}
	if o.LogDir == "" {
		o.LogDir = t.TempDir()
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s, logs
}

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// payload is a delivery as Beacon would send it, at the given time.
func payload(delivery, event string, at time.Time) []byte {
	v := map[string]any{
		"delivery": delivery, "event": event, "at": at.UTC().Format(time.RFC3339Nano), "hook": "claude-oncall",
		"alert": map[string]any{"id": "0cdfbada62dd", "status": "firing", "severity": "critical",
			"title": "git.example.com is down", "service": "git-example-com", "alerts": 1},
		"links": map[string]string{
			"alert":    "https://status.example.com/api/v1/alerts/0cdfbada62dd",
			"evidence": "https://status.example.com/api/v1/alerts/0cdfbada62dd/evidence",
			"timeline": "https://status.example.com/api/v1/alerts/0cdfbada62dd/timeline",
			"mcp":      "https://status.example.com/mcp",
		},
		"hint": "Beacon woke you for this alert group.",
	}
	b, _ := json.Marshal(v)
	return b
}

// post delivers body with the headers Beacon sets; headers may add or override some.
func post(s *Server, body []byte, sig string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/hooks/beacon", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Beacon-Signature", sig)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestGenuine(t *testing.T) {
	body := []byte(`{"delivery":"a"}`)
	good := sign(testSecret, body)
	for _, c := range []struct {
		name   string
		secret []byte
		body   []byte
		sig    string
		want   bool
	}{
		{"right", testSecret, body, good, true},
		{"upper-case hex", testSecret, body, "sha256=" + strings.ToUpper(strings.TrimPrefix(good, "sha256=")), true},
		{"other secret", []byte("other"), body, good, false},
		{"other body", testSecret, []byte(`{"delivery":"b"}`), good, false},
		{"no prefix", testSecret, body, strings.TrimPrefix(good, "sha256="), false},
		{"not hex", testSecret, body, "sha256=zz", false},
		{"empty", testSecret, body, "", false},
	} {
		if got := Genuine(c.secret, c.body, c.sig); got != c.want {
			t.Errorf("%s: Genuine = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSignatureChecked(t *testing.T) {
	s, logs := newServer(t, Options{})
	now := time.Date(2026, 11, 2, 2, 0, 31, 0, time.UTC)
	s.now = func() time.Time { return now }
	body := payload("9f3c2a1b7e4d5c60", "resolved", now.Add(-time.Minute)) // resolved: nothing to run, so this test needs no shell

	if rec := post(s, body, sign(testSecret, body), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("genuine: %d %s", rec.Code, rec.Body)
	}
	if rec := post(s, body, sign([]byte("wrong"), body), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d, want 401", rec.Code)
	}
	if rec := post(s, body, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unsigned: %d, want 401", rec.Code)
	}
	tampered := bytes.Replace(body, []byte("resolved"), []byte("firing"), 1)
	if rec := post(s, tampered, sign(testSecret, body), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered body: %d, want 401", rec.Code)
	}
	big := make([]byte, MaxBody+1)
	if rec := post(s, big, sign(testSecret, big), nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d, want 413", rec.Code)
	}
	if l := logs.String(); strings.Contains(l, "git.example.com") || strings.Contains(l, "sha256=") {
		t.Errorf("the log carries the body or the signature:\n%s", l)
	}
	if !strings.Contains(logs.String(), `reason="bad signature"`) || !strings.Contains(logs.String(), `reason="no signature"`) {
		t.Errorf("refusals not logged:\n%s", logs.String())
	}
}

func TestRefusals(t *testing.T) {
	s, _ := newServer(t, Options{})
	now := time.Date(2026, 11, 2, 2, 0, 31, 0, time.UTC)
	s.now = func() time.Time { return now }
	fresh := now.Add(-time.Minute)
	for _, c := range []struct {
		name    string
		body    []byte
		headers map[string]string
		want    int
	}{
		{"not JSON", []byte("{"), nil, http.StatusBadRequest},
		{"no delivery", payload("", "firing", fresh), nil, http.StatusBadRequest},
		{"odd delivery", payload("../x", "firing", fresh), nil, http.StatusBadRequest},
		{"unknown event", payload("d1", "pending", fresh), nil, http.StatusBadRequest},
		{"no at", payload("d1", "firing", time.Time{}), nil, http.StatusBadRequest},
		{"delivery header differs", payload("d1", "resolved", fresh), map[string]string{"X-Beacon-Delivery": "d2"}, http.StatusBadRequest},
		{"event header differs", payload("d1", "resolved", fresh), map[string]string{"X-Beacon-Event": "firing"}, http.StatusBadRequest},
		{"stale", payload("d1", "resolved", now.Add(-maxAge-time.Second)), nil, http.StatusUnauthorized},
		{"just in time", payload("d1", "resolved", now.Add(-maxAge+time.Second)), nil, http.StatusAccepted},
		{"headers agree", payload("d2", "resolved", fresh), map[string]string{"X-Beacon-Delivery": "d2", "X-Beacon-Event": "resolved"}, http.StatusAccepted},
	} {
		if rec := post(s, c.body, sign(testSecret, c.body), c.headers); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, strings.TrimSpace(rec.Body.String()), c.want)
		}
	}
}

func TestSeenDeliveries(t *testing.T) {
	s, _ := newServer(t, Options{})
	now := time.Date(2026, 11, 2, 2, 0, 31, 0, time.UTC)
	s.now = func() time.Time { return now }

	body := payload("d1", "resolved", now)
	if rec := post(s, body, sign(testSecret, body), nil); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "nothing to run") {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	if rec := post(s, body, sign(testSecret, body), nil); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "seen") {
		t.Fatalf("repeat: %d %s", rec.Code, rec.Body)
	}
	// A day later the id is forgotten, and a fresh delivery with it is taken again.
	now = now.Add(seenFor + time.Minute)
	body = payload("d1", "resolved", now)
	if rec := post(s, body, sign(testSecret, body), nil); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "nothing to run") {
		t.Fatalf("after a day: %d %s", rec.Code, rec.Body)
	}
	if _, ok := s.seen["d1"]; !ok || len(s.seen) != 1 {
		t.Errorf("seen = %v", s.seen)
	}
}

func TestRoutes(t *testing.T) {
	s, _ := newServer(t, Options{})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Errorf("healthz: %d %q", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hooks/beacon", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET elsewhere: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/hooks/beacon", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: %d", rec.Code)
	}
}

func TestEnvironment(t *testing.T) {
	var p Payload
	if err := json.Unmarshal(payload("9f3c", "firing", time.Now()), &p); err != nil {
		t.Fatal(err)
	}
	p.Links.Runbook = "https://github.com/example/ops/blob/main/runbooks/ServiceDown.md"
	p.Alert.Title = "git.example.com\x1b[31m is\ndown\x7f"
	want := []string{
		"BEACON_EVENT=firing", "BEACON_DELIVERY=9f3c", "BEACON_HOOK=claude-oncall", "BEACON_ALERT_ID=0cdfbada62dd",
		"BEACON_ALERT_TITLE=git.example.com[31m isdown", "BEACON_ALERT_SERVICE=git-example-com",
		"BEACON_ALERT_SEVERITY=critical", "BEACON_ALERT_STATUS=firing",
		"BEACON_ALERT_URL=https://status.example.com/api/v1/alerts/0cdfbada62dd",
		"BEACON_EVIDENCE_URL=https://status.example.com/api/v1/alerts/0cdfbada62dd/evidence",
		"BEACON_TIMELINE_URL=https://status.example.com/api/v1/alerts/0cdfbada62dd/timeline",
		"BEACON_MCP_URL=https://status.example.com/mcp",
		"BEACON_RUNBOOK_URL=https://github.com/example/ops/blob/main/runbooks/ServiceDown.md",
	}
	if got := Environment(p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Environment:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A group of several services, and nothing else: every variable is still there.
	var q Payload
	q.Alert.Services = []string{"a", "b"}
	got := Environment(q)
	if len(got) != len(want) || got[5] != "BEACON_ALERT_SERVICE=a,b" || got[12] != "BEACON_RUNBOOK_URL=" {
		t.Errorf("sparse payload: %v", got)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 2, 2, 0, 31, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	for name, at := range map[string]time.Time{"20261001T020031Z-old.log": old, "20261102T015031Z-new.log": now.Add(-10 * time.Minute), "notes.txt": old} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{o: Options{LogDir: dir, Keep: 720 * time.Hour}, log: slog.New(slog.NewTextHandler(&syncBuf{}, nil)), now: func() time.Time { return now }}
	s.prune()
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := "20261102T015031Z-new.log notes.txt"; strings.Join(names, " ") != want {
		t.Errorf("left %v, want %s", names, want)
	}
	// Keep 0 removes nothing.
	s.o.Keep = 0
	s.now = func() time.Time { return now.Add(365 * 24 * time.Hour) }
	s.prune()
	if entries, _ = os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("keep 0 removed logs: %d left", len(entries))
	}
}

func TestNewChecksOptions(t *testing.T) {
	dir := t.TempDir()
	for name, o := range map[string]Options{
		"no secret":  {Run: "true", LogDir: dir},
		"no program": {Secret: testSecret, LogDir: dir},
		"no log dir": {Secret: testSecret, Run: "true"},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("%s: New accepted the options", name)
		}
	}
	s, err := New(Options{Secret: testSecret, Run: "true", LogDir: filepath.Join(dir, "a", "b"), Log: slog.New(slog.NewTextHandler(&syncBuf{}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Shutdown(context.Background()) }()
	if s.o.Concurrency != 2 || s.o.Queue != DefaultQueue || s.o.Timeout != 30*time.Minute {
		t.Errorf("defaults: %+v", s.o)
	}
	if st, err := os.Stat(filepath.Join(dir, "a", "b")); err != nil || !st.IsDir() {
		t.Error("the log directory was not created")
	}
}
