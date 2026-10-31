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
	mu      sync.Mutex
	pushes  []string
	status  int // for pushes; 0 = 204
	targets string
}

func (g *gateway) server(t *testing.T, target string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer lbt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/modules":
			_, _ = io.WriteString(w, "modules:\n  http_2xx:\n    prober: http\n    timeout: 5s\n    http: {preferred_ip_protocol: ip4}\n")
		case "/api/v1/targets":
			g.mu.Lock()
			body := g.targets
			g.mu.Unlock()
			if body == "" {
				body = `[{"targets":["` + target + `/ok","` + target + `/broken"],"labels":{"module":"http_2xx","service":"web","severity":"critical"}}]`
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
		default:
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
	_ = os.WriteFile(filepath.Join(text, "heartbeat_backup.prom"), []byte(`linkos_heartbeat_last_success_timestamp_seconds{heartbeat="backup"} 1793436000`+"\n"), 0o600)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	a := New(Config{Gateway: srv.URL + "/", Token: "lbt_test", StateDir: state, Host: &hostmetrics.Collector{ProcRoot: proc, TextfileDir: text},
		Now: func() time.Time { now = now.Add(time.Millisecond); return now }})
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
		`linkos_heartbeat_last_success_timestamp_seconds{heartbeat="backup",job="node"} 1793436000 `,
		`linkos_probe_targets{job="probe-agent"} 2 `,
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
		if s.Name == "linkos_probe_last_push_timestamp_seconds" {
			last = s.Value
		}
	}
	if last != 1793491200 {
		t.Errorf("status after a push: %+v", a.Status())
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
