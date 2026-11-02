package probenow

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/targets"
)

// clock is a fake time that tests advance.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newBoard has three locations (edge-1 and edge-3 overseas, edge-2 mainland, whose machine has
// expired) and sets for hub (two modules), api (overseas), cn (mainland), docs and a service of 21.
func newBoard(t *testing.T) (*Board, *clock, *metrics.Metrics) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.yaml"), []byte(`
locations:
  - {id: edge-1, name: United States - Los Angeles, region: overseas}
  - {id: edge-2, name: Mainland China - Shanghai, region: mainland}
  - {id: edge-3, name: Germany - Frankfurt, region: overseas}
resources:
  - {name: edge-2, kind: vps, expires: 2026-10-01}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("https://m%d.example.com/", i)
	}
	set, err := targets.Build(map[string][]targets.Group{
		"all-web.yaml":      {{Targets: []string{"https://hub.example.com/"}, Labels: map[string]string{"module": "http_head", "service": "hub"}}},
		"all-tls.yaml":      {{Targets: []string{"https://hub.example.com/"}, Labels: map[string]string{"module": "tls_expiry", "service": "hub"}}},
		"all-docs.yaml":     {{Targets: []string{"https://docs.example.com/", "https://www.example.com/"}, Labels: map[string]string{"module": "http_head", "service": "docs"}}},
		"overseas-api.yaml": {{Targets: []string{"https://api.example.com/"}, Labels: map[string]string{"module": "http_2xx", "service": "api"}}},
		"mainland-cn.yaml":  {{Targets: []string{"https://cn.example.com/"}, Labels: map[string]string{"module": "http_head", "service": "cn"}}},
		"all-many.yaml":     {{Targets: many, Labels: map[string]string{"module": "http_head", "service": "many"}}},
	}, []string{"edge-1", "edge-2", "edge-3"})
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	m := metrics.New("test")
	b := New(Deps{Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return set }, Metrics: m, Now: c.Now})
	return b, c, m
}

func scrape(m *metrics.Metrics) string {
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestCreateResolvesTargetsAndLocations(t *testing.T) {
	b, _, _ := newBoard(t)
	v, err := b.Create("claude-code", []Want{{Target: "https://hub.example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	var mods []string
	for _, tg := range v.Targets {
		mods = append(mods, tg.Module)
	}
	slices.Sort(mods)
	if strings.Join(mods, ",") != "http_head,tls_expiry" || v.Targets[0].Service != "hub" || v.Targets[0].Instance != "https://hub.example.com/" {
		t.Fatalf("both modules of the target: %+v", v.Targets)
	}
	// edge-2 has expired: not expected
	if strings.Join(v.Expected, ",") != "edge-1,edge-3" || strings.Join(v.Missing, ",") != "edge-1,edge-3" || v.Done {
		t.Fatalf("expected %v missing %v done %v", v.Expected, v.Missing, v.Done)
	}
	if v.ID == "" || v.Expires.Sub(v.Created) != TTL {
		t.Errorf("id %q, lives %v", v.ID, v.Expires.Sub(v.Created))
	}
	// one module; a service; a target that one region alone probes
	if v, err := b.Create("x", []Want{{Target: "https://docs.example.com/", Module: "http_head"}}); err != nil || len(v.Targets) != 1 {
		t.Fatalf("with a module: %+v %v", v, err)
	}
	// a service: every target of it; docs.example.com was requested just now, which fails the whole request
	if _, err := b.Create("x", []Want{{Service: "docs"}}); !errors.Is(err, ErrBusy) {
		t.Fatalf("docs again at once must be busy: %v", err)
	}
	if v, err := b.Create("x", []Want{{Service: "api"}}); err != nil || len(v.Targets) != 1 || strings.Join(v.Expected, ",") != "edge-1,edge-3" {
		t.Fatalf("a service: %+v %v", v, err)
	}
}

func TestCreateErrors(t *testing.T) {
	b, c, _ := newBoard(t)
	if _, err := b.Create("x", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("nothing to probe: %v", err)
	}
	if _, err := b.Create("x", []Want{{Target: "https://nope.example.com/"}}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "no target set has target https://nope.example.com/") {
		t.Errorf("unknown target: %v", err)
	}
	if _, err := b.Create("x", []Want{{Target: "https://hub.example.com/", Module: "icmp"}}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "with module icmp") {
		t.Errorf("unknown module: %v", err)
	}
	if _, err := b.Create("x", []Want{{Target: "https://cn.example.com/"}}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "no active location probes target https://cn.example.com/") {
		t.Errorf("no active location: %v", err)
	}
	if _, err := b.Create("x", []Want{{Service: "nope"}}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "service nope") {
		t.Errorf("unknown service: %v", err)
	}
	if _, err := b.Create("x", []Want{{Service: "many"}}); !errors.Is(err, ErrTooMany) || !strings.Contains(err.Error(), "21 targets") {
		t.Errorf("too many: %v", err)
	}
	// one request per target every 15 seconds
	if _, err := b.Create("x", []Want{{Target: "https://api.example.com/"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Create("y", []Want{{Target: "https://api.example.com/"}}); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "try again in 15s") {
		t.Errorf("the same target at once: %v", err)
	}
	c.Advance(PerTarget)
	if _, err := b.Create("y", []Want{{Target: "https://api.example.com/"}}); err != nil {
		t.Errorf("after %v: %v", PerTarget, err)
	}
	// three requests in flight per token
	for _, tg := range []string{"https://hub.example.com/", "https://docs.example.com/"} {
		if _, err := b.Create("y", []Want{{Target: tg}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Create("y", []Want{{Target: "https://www.example.com/"}}); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "3 requests in flight") {
		t.Errorf("a fourth: %v", err)
	}
	if _, err := b.Create("z", []Want{{Target: "https://www.example.com/"}}); err != nil {
		t.Errorf("another token: %v", err)
	}
	if n := b.Open(); n != 5 {
		t.Errorf("open %d", n)
	}
}

func TestWaitAndAnswer(t *testing.T) {
	b, c, m := newBoard(t)
	ctx := context.Background()
	if got := b.Wait(ctx, "edge-1", 0); len(got) != 0 {
		t.Fatalf("nothing pending: %v", got)
	}
	// a long-poll hanging is woken by a request for its location, and not by one for another
	woke := make(chan []targets.Request, 1)
	go func() { woke <- b.Wait(ctx, "edge-1", 5*time.Second) }()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	v, err := b.Create("claude-code", []Want{{Target: "https://api.example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-woke:
		if time.Since(start) > time.Second || len(got) != 1 || got[0].ID != v.ID || len(got[0].Groups) != 1 ||
			got[0].Groups[0].Targets[0] != "https://api.example.com/" || got[0].Groups[0].Labels["module"] != "http_2xx" || got[0].Groups[0].Labels["service"] != "api" {
			t.Fatalf("woken after %v with %+v", time.Since(start), got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the poll was not woken")
	}
	// expected locations only: edge-2 has nothing, and the wait passes
	start = time.Now()
	if got := b.Wait(ctx, "edge-2", 30*time.Millisecond); len(got) != 0 || time.Since(start) < 30*time.Millisecond {
		t.Fatalf("edge-2: %v after %v", got, time.Since(start))
	}
	// a cancelled poll returns at once
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := b.Wait(cctx, "edge-2", time.Minute); len(got) != 0 {
		t.Fatalf("cancelled: %v", got)
	}
	// edge-3 is expected too, and still pending after edge-1 answers
	if got := b.Wait(ctx, "edge-3", 0); len(got) != 1 {
		t.Fatalf("edge-3 pending: %v", got)
	}
	results := []targets.TargetResult{
		{Target: "https://api.example.com/", Module: "http_2xx", Success: false, Reason: "http_status", DurationSeconds: 0.2, HTTPStatus: 502, Attempts: 1},
		{Target: "https://api.example.com/", Module: "http_2xx", Success: true},  // a second result for the same target is dropped
		{Target: "https://evil.example.com/", Module: "http_2xx", Success: true}, // not the request's
	}
	if err := b.Answer(v.ID, "edge-1", results); err != nil {
		t.Fatal(err)
	}
	if got := b.Wait(ctx, "edge-1", 0); len(got) != 0 {
		t.Fatalf("answered, so no longer pending: %v", got)
	}
	if err := b.Answer(v.ID, "edge-1", results); err != nil {
		t.Fatalf("a second answer changes nothing: %v", err)
	}
	if err := b.Answer("0000000000000000", "edge-1", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown request: %v", err)
	}
	// awaiting with a deadline: edge-3 is missing
	actx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	got, ok := b.Await(actx, v.ID)
	if !ok || got.Done || strings.Join(got.Missing, ",") != "edge-3" || len(got.Results) != 1 || got.Results[0].Location != "edge-1" ||
		got.Results[0].Reason != "http_status" || got.Results[0].HTTPStatus != 502 || got.Results[0].Received != c.Now() {
		t.Fatalf("await: %+v %v", got, ok)
	}
	if n := b.Open(); n != 0 {
		t.Errorf("settled requests are not in flight: %d", n)
	}
	// a late answer completes it
	if err := b.Answer(v.ID, "edge-3", []targets.TargetResult{{Target: "https://api.example.com/", Module: "http_2xx", Success: true, HTTPStatus: 200}}); err != nil {
		t.Fatal(err)
	}
	got, _ = b.Await(ctx, v.ID)
	if !got.Done || len(got.Missing) != 0 || len(got.Results) != 2 {
		t.Fatalf("complete: %+v", got)
	}
	if _, ok := b.Await(ctx, "0000000000000000"); ok {
		t.Error("awaiting an unknown request")
	}
	for _, want := range []string{"linkos_probe_now_requests_total 1", `linkos_probe_now_answers_total{location="edge-1"} 1`,
		`linkos_probe_now_answers_total{location="edge-3"} 1`, `linkos_probe_now_missing_total{location="edge-3"} 1`, "linkos_probe_now_requests_open 0"} {
		if !strings.Contains(scrape(m), want) {
			t.Errorf("metrics lack %s:\n%s", want, scrape(m))
		}
	}
	// expiry
	c.Advance(TTL)
	if err := b.Answer(v.ID, "edge-1", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("after %v: %v", TTL, err)
	}
	if _, ok := b.Await(ctx, v.ID); ok {
		t.Error("an expired request is gone")
	}
}
