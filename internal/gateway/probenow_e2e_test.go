package gateway

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/agent"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/testutil"
)

// gunzip is the body of a push as the probe wrote it, or s itself when it is not compressed.
func gunzip(s string) string {
	zr, err := gzip.NewReader(strings.NewReader(s))
	if err != nil {
		return s
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return s
	}
	return string(b)
}

// The whole loop: the real probe agent polls this gateway, an agent at the centre calls
// POST /api/v1/probe-now, the probe probes the target at once, posts what it found and pushes the
// samples, and the call returns the location's answer.
func TestProbeNowEndToEnd(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(svc.Close)
	cat := &catalog.Catalog{Locations: []catalog.Location{{ID: "edge-1", Name: "United States - Los Angeles", Region: "overseas"}}}
	set, err := targets.Build(map[string][]targets.Group{"all-web.yaml": {{Targets: []string{svc.URL + "/ok", svc.URL + "/broken"},
		Labels: map[string]string{"module": "http_2xx", "service": "web"}}}}, []string{"edge-1"})
	if err != nil {
		t.Fatal(err)
	}
	board := probenow.New(probenow.Deps{Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return set }})
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.ProbeNow = board })
	auth := &api.Auth{Store: e.Store}

	// the gateway, with a sink in VictoriaMetrics' place
	sink := &seen{}
	vmURL, _ := url.Parse(sink.server(t).URL)
	modules := "modules:\n  http_2xx:\n    prober: http\n    timeout: 5s\n    http: {preferred_ip_protocol: ip4}\n"
	gw := httptest.NewServer(Handler(Deps{Auth: auth, Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return set },
		Modules: func() []byte { return []byte(modules) }, VM: vmURL, Requests: board}))
	t.Cleanup(gw.Close)

	// the API agents call
	mux := http.NewServeMux()
	(&api.Server{Bridge: e.Bridge, Auth: auth, Log: slog.Default()}).Register(mux)
	apiSrv := httptest.NewServer(mux)
	t.Cleanup(apiSrv.Close)
	write := e.Token(t, "claude-code", "alerts:write")

	// the real probe agent, enrolled for edge-1, running its cycle once an hour and polling
	probeToken, _, err := e.Store.CreateSubjectToken("probe-edge-1", "edge-1", []string{"probe:write"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	ag := agent.New(agent.Config{Gateway: gw.URL, Token: probeToken, StateDir: t.TempDir(), Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { ag.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	post := func(body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, apiSrv.URL+"/api/v1/probe-now", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+write)
		req.Header.Set("Content-Type", "application/json")
		resp, err := apiSrv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}

	start := time.Now()
	code, out := post(`{"target":"` + svc.URL + `/broken","wait":20}`)
	if code != http.StatusOK {
		t.Fatalf("probe-now: %d %v", code, out)
	}
	results, _ := out["results"].([]any)
	if len(results) != 1 || out["complete"] != true || out["summary"] != "1 failing (http_status)" {
		t.Fatalf("answer after %v: %v", time.Since(start), out)
	}
	r := results[0].(map[string]any)
	if r["location"] != "edge-1" || r["ok"] != false || r["reason"] != "http_status" || r["http_status"] != 502.0 || r["attempts"] != 1.0 || r["probed_at"] == nil {
		t.Fatalf("result %v", r)
	}
	if missing, _ := out["missing"].([]any); len(missing) != 0 {
		t.Errorf("missing %v", missing)
	}
	// the probe pushed the fresh samples through the gateway as in its cycle: the cycle's push and the request's
	want := `probe_success{instance="` + svc.URL + `/broken",job="probe",module="http_2xx",service="web"} 0 `
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := 0
		sink.mu.Lock()
		for _, b := range sink.body {
			if strings.Contains(gunzip(b), want) { // pushed compressed, and forwarded as is
				n++
			}
		}
		sink.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fresh samples did not arrive: %d pushes with the target", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// the target was requested just now: busy; the other target is fine
	if code, out := post(`{"service":"web","wait":5}`); code != http.StatusTooManyRequests || out["code"] != "probe.busy" {
		t.Errorf("busy: %d %v", code, out)
	}
	if code, out := post(`{"target":"` + svc.URL + `/ok","wait":20}`); code != http.StatusOK || out["summary"] != "1 OK" || out["complete"] != true {
		t.Errorf("the working target: %d %v", code, out)
	}
	if last := e.Bot.Texts[len(e.Bot.Texts)-1]; last != "claude-code had 1 locations probe "+svc.URL+"/ok again via API: 1 OK" {
		t.Errorf("echo %q", last)
	}
	if a := e.Store.AuditLog(2); len(a) != 2 || a[0].Action != "probe_now" || a[1].Action != "probe_now" || a[1].Detail != "1 failing (http_status)" {
		t.Errorf("audit %+v", a)
	}
}
