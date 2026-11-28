package gateway

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/agent"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/exercise"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/probe"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/server"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestExerciseObservationThroughTheRegularProbeCycle(t *testing.T) {
	env := testutil.New(t)
	key, err := env.Store.Secret("drill-observations")
	if err != nil {
		t.Fatal(err)
	}
	authority := exercise.New(key)
	auth := &api.Auth{Store: env.Store}
	centre := server.Handler(server.Deps{
		Bridge: env.Bridge, API: &api.Server{Bridge: env.Bridge, Auth: auth, Log: slog.Default()},
		MCP: http.NotFoundHandler(), Metrics: metrics.New("test"), DrillRun: env.Store.DrillRun, Exercise: authority, Log: slog.Default(),
	})
	var ordinaryFailure atomic.Bool
	var capturedReceipt atomic.Value
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ordinaryFailure.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "upstream unavailable")
			return
		}
		// Capture the origin's header without changing the probe transport.
		out := httptest.NewRecorder()
		centre.ServeHTTP(out, r)
		for name, values := range out.Header() {
			w.Header()[name] = values
		}
		capturedReceipt.Store(out.Header().Get(exercise.ReceiptHeader))
		w.WriteHeader(out.Code)
		_, _ = w.Write(out.Body.Bytes())
	}))
	t.Cleanup(target.Close)
	targetURL := target.URL + exercise.Path
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	cat := &catalog.Catalog{Locations: []catalog.Location{{ID: "edge-1", Region: "overseas"}, {ID: "edge-2", Region: "overseas"}}}
	labels := map[string]string{"module": "http_2xx", "service": "drill", "severity": "critical", "failure_domain": "monitoring"}
	set, err := targets.Build(map[string][]targets.Group{"all-drill.yaml": {{Targets: []string{targetURL}, Labels: labels}}}, []string{"edge-1", "edge-2"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &seen{}
	vmURL, _ := url.Parse(sink.server(t).URL)
	gw := httptest.NewServer(Handler(Deps{
		Auth: auth, Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return set },
		Modules: func() []byte {
			return []byte("modules:\n  http_2xx:\n    prober: http\n    timeout: 5s\n    http: {preferred_ip_protocol: ip4}\n")
		},
		VM: vmURL, Log: slog.Default(), ExerciseTarget: targetURL, Exercise: authority,
	}))
	t.Cleanup(gw.Close)
	edge := func(location string) (*agent.Agent, string) {
		t.Helper()
		token, _, err := env.Store.CreateSubjectToken("probe-"+location, location, []string{"probe:write"}, time.Hour, "test")
		if err != nil {
			t.Fatal(err)
		}
		return agent.New(agent.Config{Gateway: gw.URL, Token: token, StateDir: t.TempDir(), Prober: &probe.Prober{RootCAs: roots}}), token
	}
	first, token := edge("edge-1")
	second, other := edge("edge-2")
	cycle := func(a *agent.Agent) {
		t.Helper()
		if err := a.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	cycle(first)
	now := time.Now()
	run, _, err := env.Store.SetDrill(now, now.Add(time.Minute), "fixture", "1m")
	if err != nil || run.ID == "" {
		t.Fatalf("arm: %+v %v", run, err)
	}

	// An unrelated 503 on the same URL still reaches metrics but carries no source receipt.
	ordinaryFailure.Store(true)
	cycle(first)
	if observed := env.Store.DrillObservedLocations(run.ID); len(observed) != 0 {
		t.Fatal("an unrelated failure was recorded as a deliberate response")
	}
	ordinaryFailure.Store(false)
	cycle(first)
	replay := capturedReceipt.Load().(string)
	if replay == "" {
		t.Fatal("the built-in response did not carry an authenticated receipt")
	}
	post := func(token, receipt string) int {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"receipt": receipt})
		req, _ := http.NewRequest(http.MethodPost, gw.URL+exercise.ObservationPath, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := gw.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if post(other, replay) != http.StatusBadRequest || post(token, "fake") != http.StatusBadRequest || post(token, replay) != http.StatusNoContent {
		t.Fatal("wrong location, tampered receipt, or harmless replay got an unexpected result")
	}
	cycle(second)
	active, evidence, err := env.Store.DrillEvidence()
	if err != nil || active.ID != run.ID || len(evidence) != 2 || evidence[0].Location != "edge-1" || evidence[1].Location != "edge-2" {
		t.Fatalf("expected one checked observation from each location: %+v %+v %v", active, evidence, err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, v := range evidence {
		if v.URL != targetURL || v.Assignment != exercise.Assignment(labels) || v.Recorded.Before(v.Served) {
			t.Fatalf("wrong bound proof: %+v", v)
		}
		found := false
		for _, b := range sink.body {
			if strings.Contains(gunzip(b), "probe_success{failure_domain=\"monitoring\",instance=\""+targetURL+
				"\",job=\"probe\",module=\"http_2xx\",service=\"drill\",severity=\"critical\"} 0 "+strconv.FormatInt(v.Sampled.UnixMilli(), 10)+"\n") {
				found = true
			}
		}
		if !found {
			t.Fatalf("receipt did not match an exact failed probe sample: %+v", v)
		}
	}
}

func TestOnlyTheDedicatedRulesCredentialMayCarryTheExerciseAnchor(t *testing.T) {
	e := testutil.New(t)
	ordinary := e.Token(t, "rules", "alerts:send")
	alert := `[{"labels":{"alertname":"ServiceDown"},"annotations":{"beacon_rule_active_at":"946684980000","summary":"fixture"}}]`
	for _, enabled := range []bool{false, true} {
		var got []router.Postable
		deps := Deps{Auth: &api.Auth{Store: e.Store}, RulesToken: "fixture-rules-secret", Alerts: func(_ string, in []router.Postable) error {
			got = in
			return nil
		}}
		if enabled {
			deps.Exercise = exercise.New([]byte("isolated-fixture"))
		}
		h := Handler(deps)
		for _, secret := range []string{ordinary, deps.RulesToken} {
			req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", strings.NewReader(alert))
			req.Header.Set("Authorization", "Bearer "+secret)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			want := enabled && secret == deps.RulesToken
			if out.Code != http.StatusOK || len(got) != 1 || (got[0].Annotations[exercise.ActiveAtAnnotation] != "") != want ||
				got[0].Annotations["summary"] != "fixture" {
				t.Fatalf("trusted=%t fixture response %d with %+v", want, out.Code, got)
			}
		}
	}
}
