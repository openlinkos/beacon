package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/store"
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
	if a := e.Store.AuditLog(1); a[0].Action != "probe_now" || a[0].Via != "api" || a[0].Target != testutil.GroupID() || !strings.Contains(a[0].Detail, "2 OK") {
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

func TestIncidentProbeRequiresDurableEvidence(t *testing.T) {
	for _, fault := range []struct{ name, trigger string }{
		{"group", "BEFORE INSERT ON kv WHEN NEW.bucket = 'groups'"},
		{"audit", "BEFORE INSERT ON audit_events"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "evidence.db")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			board := testutil.ProbeBoard(t)
			e := testutil.NewWith(t, func(d *bridge.Deps) { d.ProbeNow, d.Store = board, st })
			e.Store = st
			write := e.Token(t, "observer", "alerts:write")
			mux := http.NewServeMux()
			(&Server{Bridge: e.Bridge, Auth: &Auth{Store: st}, Log: slog.Default()}).Register(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			c := client{t, srv}

			if err := e.Bridge.HandleWebhook(t.Context(), alertmanager.Webhook{GroupKey: testutil.GroupKey, Status: "resolved",
				CommonLabels: map[string]string{"service": "hub-example-com", "severity": "critical"}}); err != nil {
				t.Fatal(err)
			}
			g, _ := st.Group(testutil.GroupID())
			before := len(e.Bot.Texts)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if _, err := db.Exec("CREATE TRIGGER reject_write " + fault.trigger + " BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
				t.Fatal(err)
			}
			answerAs(t, board, "edge-1")
			answerAs(t, board, "edge-2")
			body := fmt.Sprintf(`{"alert_id":%q,"expected_episode":%q,"wait":5}`, g.ID, g.Episode)
			code, out := c.do("POST", "/api/v1/probe-now", write, body)
			if code != http.StatusServiceUnavailable || out["code"] != "probe.not_recorded" ||
				out["error"] != "could not save the check for this incident" {
				t.Fatalf("failed write response: %d %v", code, out)
			}
			saved, _ := st.Group(g.ID)
			outcome, err := st.IncidentOutcomeForEpisode(g.ID, g.Episode, time.Time{})
			if err != nil || len(saved.ProbeRuns) != 0 || saved.PassingCheck != nil || outcome.Verification != nil || outcome.PassingCheck != nil {
				t.Fatalf("failed write changed the group or its outcome: %+v %+v %v", saved.ProbeRuns, outcome, err)
			}
			for _, item := range st.AuditLog(20) {
				if item.Action == "probe_now" {
					t.Fatalf("failed write created a successful check audit: %+v", item)
				}
			}
			if len(e.Bot.Texts) != before {
				t.Fatalf("failed write echoed to chat: %v", e.Bot.Texts[before:])
			}
		})
	}
}
