package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/testutil"
)

// answerAs plays a probe location for the rest of the test: it polls as a probe does and answers every
// target as working.
func answerAs(t *testing.T, b *probenow.Board, location string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			for _, r := range b.Wait(ctx, location, time.Second) {
				var results []targets.TargetResult
				for _, g := range r.Groups {
					for _, tg := range g.Targets {
						results = append(results, targets.TargetResult{Target: tg, Module: g.Labels["module"], Success: true, DurationSeconds: 0.2, HTTPStatus: 200, Attempts: 1})
					}
				}
				_ = b.Answer(r.ID, location, results)
			}
		}
	}()
}

func setupProbeNow(t *testing.T) (*testutil.Env, *probenow.Board, client) {
	t.Helper()
	board := testutil.ProbeBoard(t)
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.ProbeNow = board })
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default()}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return e, board, client{t, srv}
}

func TestProbeNowRoute(t *testing.T) {
	e, board, c := setupProbeNow(t)
	read := e.Token(t, "reader", "status:read")
	write := e.Token(t, "claude-code", "status:read", "alerts:write")
	if code, out := c.do("POST", "/api/v1/probe-now", read, `{"target":"https://hub.example.com/"}`); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a read token: %d %v", code, out)
	}
	for _, body := range []string{`{}`, `{"target":"https://hub.example.com/","service":"api"}`, `{"service":"api","module":"http_2xx"}`, `{"target":"https://hub.example.com/","wait":91}`, `{"nope":1}`} {
		if code, out := c.do("POST", "/api/v1/probe-now", write, body); code != 400 || out["code"] != "bad_input" {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	if code, out := c.do("POST", "/api/v1/probe-now", write, `{"target":"https://nope.example.com/","wait":1}`); code != 404 || out["code"] != "probe.not_found" {
		t.Errorf("unknown target: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/probe-now", write, `{"alert_id":"nope","wait":1}`); code != 404 || out["code"] != "alert.not_found" {
		t.Errorf("unknown alert: %d %v", code, out)
	}
	// api.example.com is edge-1's alone, and nobody answers
	code, out := c.do("POST", "/api/v1/probe-now", write, `{"target":"https://api.example.com/","wait":1}`)
	if code != 200 || out["complete"] != false || out["summary"] != "1 did not answer" {
		t.Fatalf("missing: %d %v", code, out)
	}
	if missing, _ := out["missing"].([]any); len(missing) != 1 || missing[0] != "edge-1" {
		t.Errorf("missing %v", out["missing"])
	}
	if locs, _ := out["locations"].([]any); len(locs) != 1 || locs[0] != "edge-1" {
		t.Errorf("locations %v", out["locations"])
	}
	if results, _ := out["results"].([]any); len(results) != 0 {
		t.Errorf("results %v", out["results"])
	}
	if code, out := c.do("POST", "/api/v1/probe-now", write, `{"target":"https://api.example.com/","wait":1}`); code != 429 || out["code"] != "probe.busy" {
		t.Errorf("the same target at once: %d %v", code, out)
	}
	// the alert's service has one target in two modules, probed from both locations
	answerAs(t, board, "edge-1")
	answerAs(t, board, "edge-2")
	code, out = c.do("POST", "/api/v1/probe-now", write, `{"alert_id":"`+testutil.GroupID()+`","wait":5}`)
	results, _ := out["results"].([]any)
	tgs, _ := out["targets"].([]any)
	if code != 200 || out["complete"] != true || len(results) != 4 || len(tgs) != 2 || out["summary"] != "2 OK" {
		t.Fatalf("by alert: %d %v", code, out)
	}
	r := results[0].(map[string]any)
	if r["ok"] != true || r["http_status"] != 200.0 || r["duration_seconds"] != 0.2 || r["probed_at"] == nil || r["instance"] != "https://hub.example.com/" {
		t.Errorf("result %v", r)
	}
	if last := e.Bot.Texts[len(e.Bot.Texts)-1]; last != "claude-code had 2 locations probe 2 targets of hub-example-com again via API: 2 OK" {
		t.Errorf("echo %q", last)
	}
	if a := e.Store.AuditLog(1); a[0].Action != "probe_now" || a[0].Via != "api" || a[0].Target != "2 targets of hub-example-com" {
		t.Errorf("audit %+v", a[0])
	}
}

func TestProbeNowPerTokenLimit(t *testing.T) {
	e, board, c := setupProbeNow(t)
	write := e.Token(t, "claude-code", "alerts:write")
	for _, tg := range []string{"https://docs.example.com/", "https://www.example.com/", "https://api.example.com/"} {
		if _, err := board.Create("claude-code", []probenow.Want{{Target: tg}}); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := c.do("POST", "/api/v1/probe-now", write, `{"target":"https://hub.example.com/","wait":1}`); code != 429 || out["code"] != "probe.busy" || !strings.Contains(out["error"].(string), "3 requests in flight") {
		t.Errorf("a fourth request: %d %v", code, out)
	}
	other := e.Token(t, "codex", "alerts:write")
	if code, _ := c.do("POST", "/api/v1/probe-now", other, `{"target":"https://hub.example.com/","wait":1}`); code != 200 {
		t.Errorf("another token: %d", code)
	}
}

func TestProbeNowUnavailable(t *testing.T) {
	e, c := setup(t)
	write := e.Token(t, "claude-code", "alerts:write")
	if code, out := c.do("POST", "/api/v1/probe-now", write, `{"target":"https://hub.example.com/"}`); code != 503 || out["code"] != "probe.unavailable" {
		t.Errorf("without a gateway: %d %v", code, out)
	}
}
