package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestLateAgentReportRemainsOnTheOriginatingIncident(t *testing.T) {
	e := testutil.WithAgentRun(t)
	c := serveTestAPI(t, e)
	runner := e.Token(t, "runner", bridge.AgentRunScope)
	other := e.Token(t, "other", bridge.AgentRunScope)
	readonly := e.Token(t, "history-reader", "status:read")
	operator := e.Token(t, "operator", "alerts:write")
	group := testutil.GroupID()
	path := "/api/v1/alerts/" + group + "/agent-runs/"
	const terminal = `{"status":"failed","exit_code":79}`
	if code, body := c.do(http.MethodPut, path+"test-delivery", runner, `{"status":"running"}`); code != 200 || body["status"] != "running" {
		t.Fatalf("the current reporter could not record the start: %d %v", code, body)
	}
	g, _ := e.Store.Group(group)
	g.Agents = append(g.Agents, store.AgentCall{Hook: "agent-run", Event: "firing", Delivery: "default-without-reporter", Status: "pending"})
	if err := e.Store.PutGroup(g); err != nil {
		t.Fatal(err)
	}

	webhook := alertmanager.Webhook{GroupKey: testutil.GroupKey, Status: "resolved",
		CommonLabels: map[string]string{"severity": "critical", "service": "hub-example-com"}}
	ctx := context.Background()
	if err := e.Bridge.HandleWebhook(ctx, webhook); err != nil {
		t.Fatal(err)
	}
	webhook.Status = "firing"
	if err := e.Bridge.HandleWebhook(ctx, webhook); err != nil {
		t.Fatal(err)
	}
	if code, _ := c.do(http.MethodPost, "/api/v1/alerts/"+group+"/ack", operator, `{"note":"Looking into this recurrence"}`); code != http.StatusOK {
		t.Fatalf("could not acknowledge the replacement incident: %d", code)
	}
	for _, tc := range []struct {
		delivery, token, code string
		want                  int
	}{
		{"test-delivery", "", "auth.required", 401},
		{"test-delivery", readonly, "auth.scope", 403},
		{"test-delivery", other, "agent_run.not_found", 404},
		{"unknown-delivery", runner, "agent_run.not_found", 404},
		{"default-without-reporter", runner, "agent_run.not_found", 404},
	} {
		if status, body := c.do(http.MethodPut, path+tc.delivery, tc.token, terminal); status != tc.want || body["code"] != tc.code {
			t.Errorf("archived report guard for %s: %d %v", tc.delivery, status, body)
		}
	}
	var recorded any
	for i, input := range []string{terminal, terminal, `{"status":"running"}`} {
		status, run := c.do(http.MethodPut, path+"test-delivery", runner, input)
		if status != 200 || run["status"] != "failed" || run["exit_code"] != float64(79) {
			t.Fatalf("archived report retry %d: %d %v", i, status, run)
		}
		if i == 0 {
			recorded = run["at"]
		} else if run["at"] != recorded {
			t.Fatal("an idempotent report changed the recorded time")
		}
	}
	if status, body := c.do(http.MethodPut, path+"test-delivery", runner, `{"status":"timed_out"}`); status != 409 || body["code"] != "agent_run.conflict" {
		t.Fatalf("another terminal report overwrote history: %d %v", status, body)
	}
	g, _ = e.Store.Group(group)
	if !g.Firing() || g.Ack == nil || g.Ack.By != "operator" || len(g.Agents) != 0 {
		t.Fatalf("an older report changed the replacement incident: %+v", g)
	}
	code, body := c.do(http.MethodGet, "/api/v1/incident-outcomes?group="+group, readonly, "")
	if code != 200 {
		t.Fatalf("read compact history: %d %v", code, body)
	}
	outcome := body["outcomes"].([]any)[0].(map[string]any)
	d := outcome["agent_deliveries"].([]any)[0].(map[string]any)
	if d["run"].(map[string]any)["status"] != "failed" || d["started_at"] == nil {
		t.Fatalf("the compact history lost the start or outcome: %+v", d)
	}
	if raw, err := json.Marshal(body); err != nil || strings.Contains(string(raw), "token:runner") {
		t.Fatal("public outcome JSON exposed the private reporter key")
	}
	audited := 0
	for _, a := range e.Store.AuditLog(20) {
		if a.Action == "report_agent_run" && strings.Contains(a.Detail, "exit 79") {
			audited++
			if a.Target != outcome["id"] {
				t.Errorf("historical report audited under the wrong incident: %+v", a)
			}
		}
	}
	if audited != 1 {
		t.Fatalf("expected one terminal audit record, got %d", audited)
	}
}
