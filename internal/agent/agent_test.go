package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/hostmetrics"
)

type gateway struct {
	mu       sync.Mutex
	pushes   []string
	status   int // for pushes; 0 = 204
	targets  string
	settings string // GET /api/v1/agent; empty: 404, as from an older gateway
	modules  string // GET /api/v1/modules; empty: one http_2xx module

	requests       chan string       // GET /api/v1/agent/requests: a poll hangs until a list is sent; nil: 404, as from an older gateway
	requestsStatus int               // when set, the requests route answers this status instead
	polls          int               // polls seen
	answers        map[string]string // POST /api/v1/agent/requests/{id}: the body, by id
}

func (g *gateway) server(t *testing.T, target string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer lbt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/modules":
			body := g.modules
			if body == "" {
				body = "modules:\n  http_2xx:\n    prober: http\n    timeout: 5s\n    http: {preferred_ip_protocol: ip4}\n"
			}
			_, _ = io.WriteString(w, body)
		case "/api/v1/targets":
			g.mu.Lock()
			body := g.targets
			g.mu.Unlock()
			if body == "" {
				body = `[{"targets":["` + target + `/ok","` + target + `/broken"],"labels":{"module":"http_2xx","service":"web","severity":"critical"}}]`
			}
			_, _ = io.WriteString(w, body)
		case "/api/v1/agent":
			g.mu.Lock()
			body := g.settings
			g.mu.Unlock()
			if body == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, body)
		case "/api/v1/import/prometheus":
			zr, err := gzip.NewReader(r.Body)
			if err != nil || r.Header.Get("Content-Encoding") != "gzip" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(zr)
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.status != 0 {
				w.WriteHeader(g.status)
				return
			}
			g.pushes = append(g.pushes, string(b))
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/requests":
			g.mu.Lock()
			g.polls++
			st, ch := g.requestsStatus, g.requests
			g.mu.Unlock()
			switch {
			case st != 0:
				w.WriteHeader(st)
			case ch == nil:
				w.WriteHeader(http.StatusNotFound)
			default:
				select {
				case body := <-ch:
					_, _ = io.WriteString(w, body)
				case <-r.Context().Done():
				}
			}
		default:
			if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/agent/requests/"); ok && r.Method == http.MethodPost {
				b, _ := io.ReadAll(r.Body)
				g.mu.Lock()
				if g.answers == nil {
					g.answers = map[string]string{}
				}
				g.answers[id] = string(b)
				g.mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (g *gateway) take() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.pushes
	g.pushes = nil
	return out
}

func (g *gateway) set(status int) { g.mu.Lock(); g.status = status; g.mu.Unlock() }

func newAgent(t *testing.T) (*Agent, *gateway, string) {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(target.Close)
	gw := &gateway{}
	srv := gw.server(t, target.URL)
	state := t.TempDir()
	proc := t.TempDir()
	_ = os.WriteFile(filepath.Join(proc, "loadavg"), []byte("0.5 0.4 0.3 1/2 3\n"), 0o600)
	text := t.TempDir()
	_ = os.WriteFile(filepath.Join(text, "heartbeat_backup.prom"), []byte(`beacon_heartbeat_last_success_timestamp_seconds{heartbeat="backup"} 1793436000`+"\n"), 0o600)
	var mu sync.Mutex // the cycle and the request poller both ask the time
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	a := New(Config{Gateway: srv.URL + "/", Token: "lbt_test", StateDir: state, Host: &hostmetrics.Collector{ProcRoot: proc, TextfileDir: text},
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); now = now.Add(time.Millisecond); return now }})
	return a, gw, target.URL
}

func TestCyclePushesProbesHostAndHeartbeats(t *testing.T) {
	a, gw, target := newAgent(t)
	if err := a.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	pushes := gw.take()
	if len(pushes) != 1 {
		t.Fatalf("pushes %d", len(pushes))
	}
	body := pushes[0]
	for _, want := range []string{
		`probe_success{instance="` + target + `/ok",job="probe",module="http_2xx",service="web",severity="critical"} 1 1793491200`,
		`probe_success{instance="` + target + `/broken",job="probe",module="http_2xx",service="web",severity="critical"} 0 `,
		`probe_failure_info{instance="` + target + `/broken",job="probe",module="http_2xx",service="web",severity="critical",reason="http_status"} 1 `,
		`probe_http_status_code{instance="` + target + `/broken",`,
		`node_load1{job="node"} 0.5 `,
		`beacon_heartbeat_last_success_timestamp_seconds{heartbeat="backup",job="node"} 1793436000 `,
		`beacon_probe_targets{job="probe-agent"} 2 `,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if _, err := os.Stat(filepath.Join(a.cfg.StateDir, "targets.json")); err != nil {
		t.Error("targets are cached")
	}
	var last float64
	for _, s := range a.Status() {
		if s.Name == "beacon_probe_last_push_timestamp_seconds" {
			last = s.Value
		}
	}
	if last != 1793491200 {
		t.Errorf("status after a push: %+v", a.Status())
	}
}

func TestLocationTimeout(t *testing.T) {
	a, gw, _ := newAgent(t)
	// an older gateway without settings: the module's own 5s
	if err := a.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m := a.module("http_2xx"); m == nil || time.Duration(m.Timeout) != 5*time.Second {
		t.Fatalf("without settings: %+v", m)
	}
	// a location on a lossy path gets at least 45s, without changing the shared module
	gw.mu.Lock()
	gw.settings = `{"location":"cdn-pre-cn1","probe_timeout_seconds":45}`
	gw.mu.Unlock()
	if err := a.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m := a.module("http_2xx"); time.Duration(m.Timeout) != 45*time.Second || time.Duration(a.mods.Modules["http_2xx"].Timeout) != 5*time.Second {
		t.Fatalf("with 45s: %v, shared %v", m.Timeout, a.mods.Modules["http_2xx"].Timeout)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.StateDir, "agent.json")); err != nil {
		t.Error("settings are cached")
	}
	// a shorter least timeout leaves a longer module alone
	gw.mu.Lock()
	gw.settings = `{"location":"cdn-pre-cn1","probe_timeout_seconds":2}`
	gw.mu.Unlock()
	_ = a.Cycle(context.Background())
	if m := a.module("http_2xx"); time.Duration(m.Timeout) != 5*time.Second {
		t.Fatalf("with 2s: %v", m.Timeout)
	}
	if a.module("nope") != nil {
		t.Error("an unknown module")
	}
}

func TestRetriesOnlyWithoutAnAnswer(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path]++
		n := seen[r.URL.Path]
		mu.Unlock()
		switch {
		case r.URL.Path == "/flaky" && n == 1:
			time.Sleep(1500 * time.Millisecond) // past the module's 1s: a timeout, retried
		case r.URL.Path == "/wrong":
			w.WriteHeader(http.StatusBadGateway) // an answer: never retried
		}
	}))
	t.Cleanup(target.Close)
	gw := &gateway{modules: "modules:\n  quick:\n    prober: http\n    timeout: 1s\n    http: {preferred_ip_protocol: ip4}\n",
		targets:  `[{"targets":["` + target.URL + `/flaky","` + target.URL + `/wrong"],"labels":{"module":"quick","service":"web"}}]`,
		settings: `{"location":"cdn-pre-cn1","probe_retries":1}`}
	srv := gw.server(t, target.URL)
	a := New(Config{Gateway: srv.URL, Token: "lbt_test", StateDir: t.TempDir()})
	if err := a.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := gw.take()[0]
	for _, want := range []string{
		`probe_success{instance="` + target.URL + `/flaky",job="probe",module="quick",service="web"} 1 `,
		`beacon_probe_attempts{instance="` + target.URL + `/flaky",job="probe",module="quick",service="web"} 2 `,
		`probe_success{instance="` + target.URL + `/wrong",job="probe",module="quick",service="web"} 0 `,
		`beacon_probe_attempts{instance="` + target.URL + `/wrong",job="probe",module="quick",service="web"} 1 `,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["/wrong"] != 1 || seen["/flaky"] != 2 {
		t.Errorf("requests %v", seen)
	}
}

func TestQueueWhenTheGatewayFails(t *testing.T) {
	a, gw, _ := newAgent(t)
	gw.set(http.StatusBadGateway)
	for range 3 {
		if err := a.Cycle(context.Background()); err == nil {
			t.Fatal("a failed push is an error")
		}
	}
	if files, _ := a.queued(); len(files) != 3 {
		t.Fatalf("queued %d", len(files))
	}
	gw.set(0)
	if err := a.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(gw.take()); n != 4 {
		t.Fatalf("the batch and the three queued ones: %d", n)
	}
	if files, _ := a.queued(); len(files) != 0 {
		t.Fatalf("queue left %d", len(files))
	}
	// a refusal is not queued: retrying cannot fix it
	gw.set(http.StatusForbidden)
	_ = a.Cycle(context.Background())
	if files, _ := a.queued(); len(files) != 0 {
		t.Fatalf("a refused batch must not be queued: %d", len(files))
	}
}

func TestQueueLimitAndCache(t *testing.T) {
	a, gw, _ := newAgent(t)
	if err := a.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	gw.take()
	a.cfg.MaxQueue = 1 // bytes: every new batch drops the older ones
	gw.set(http.StatusBadGateway)
	_ = a.Cycle(context.Background())
	_ = a.Cycle(context.Background())
	if files, _ := a.queued(); len(files) != 0 && len(files) != 1 {
		t.Fatalf("queue over its limit: %d", len(files))
	}
	// a restarted agent that cannot reach the gateway uses its cached targets and modules
	b := New(Config{Gateway: "http://127.0.0.1:1", Token: "x", StateDir: a.cfg.StateDir, HTTP: &http.Client{Timeout: time.Second}})
	b.refresh(context.Background())
	if b.mods == nil || len(b.groups) != 1 {
		t.Fatalf("cache: mods %v groups %d", b.mods, len(b.groups))
	}
}

func TestWriteTextLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`x 1`, `x{job="node"} 1 0`},
		{`x{a="b"} 2`, `x{a="b",job="node"} 2 0`},
		{`x{} 3`, `x{job="node"} 3 0`},
	} {
		var w bytes.Buffer
		writeTextLine(&w, c.in, "node", time.UnixMilli(0))
		if got := strings.TrimSpace(w.String()); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}
