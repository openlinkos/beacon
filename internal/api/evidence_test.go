package api

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestReportAgentRunIsScopedAndBoundToItsDelivery(t *testing.T) {
	e := testutil.WithAgentRun(t)
	c := serveTestAPI(t, e)
	runner := e.Token(t, "runner", bridge.AgentRunScope)
	other := e.Token(t, "other", bridge.AgentRunScope)
	operator := e.Token(t, "operator", "alerts:write")
	path := "/api/v1/alerts/" + testutil.GroupID() + "/agent-runs/"
	result := `{"status":"failed","exit_code":79}`
	for _, tc := range []struct {
		delivery, token, body, code string
		status                      int
	}{
		{"test-delivery", "", result, "auth.required", 401},
		{"test-delivery", operator, result, "auth.scope", 403},
		{"test-delivery", other, result, "auth.scope", 403},
		{"unknown", runner, result, "agent_run.not_found", 404},
		{"test-delivery", runner, `{"status":"succeeded","exit_code":79}`, "bad_input", 400},
		{"test-delivery", runner, `{"status":"failed","exit_code":-1}`, "bad_input", 400},
	} {
		if code, out := c.do("PUT", path+tc.delivery, tc.token, tc.body); code != tc.status || out["code"] != tc.code {
			t.Errorf("want %d %s, got %d %v", tc.status, tc.code, code, out)
		}
	}
	if code, out := c.do("PUT", path+"test-delivery", runner, result); code != 200 || out["status"] != "failed" || out["exit_code"] != float64(79) || out["by"] != "runner" {
		t.Fatalf("terminal report with a pending hook response: %d %v", code, out)
	}
	if code, out := c.do("PUT", path+"test-delivery", runner, `{"status":"running"}`); code != 200 || out["status"] != "failed" {
		t.Errorf("late running report: %d %v", code, out)
	}
	if code, out := c.do("PUT", path+"test-delivery", runner, `{"status":"timed_out"}`); code != 409 || out["code"] != "agent_run.conflict" {
		t.Errorf("terminal report overwritten: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/alerts/"+testutil.GroupID()+"/ack", runner, `{}`); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("reporter can acknowledge the incident: %d %v", code, out)
	}
}

func TestEvidenceRoutes(t *testing.T) {
	e, c := setup(t)
	read := e.Token(t, "reader", "status:read")
	write := e.Token(t, "claude-code", "status:read", "alerts:write")
	id := testutil.GroupID()
	if _, err := e.Bridge.RecordChange(context.Background(), store.Change{Source: "ci", Kind: "deploy", Service: "hub-example-com", Summary: "hub v2"}, "ci"); err != nil {
		t.Fatal(err)
	}

	code, out := c.do("GET", "/api/v1/alerts/"+id+"/evidence", read, "")
	if code != 200 || out["untrusted"] == nil || out["probes"] == nil {
		t.Fatalf("evidence: %d %v", code, out)
	}
	if probes := out["probes"].(map[string]any); probes["error"] != "VictoriaMetrics is not configured" {
		t.Errorf("without VictoriaMetrics, the rest still answers: %v", probes)
	}
	if changes, _ := out["changes"].([]any); len(changes) != 1 || changes[0].(map[string]any)["related"] != "service" {
		t.Errorf("changes: %v", out["changes"])
	}
	if code, out := c.do("GET", "/api/v1/alerts/"+id+"/evidence?before=forever", read, ""); code != 400 || out["code"] != "bad_input" {
		t.Errorf("bad before: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/alerts/nope/evidence", read, ""); code != 404 || out["code"] != "alert.not_found" {
		t.Errorf("unknown alert: %d %v", code, out)
	}

	if code, out := c.do("POST", "/api/v1/alerts/"+id+"/notes", read, `{"text":"x"}`); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a read token cannot leave notes: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/alerts/"+id+"/notes", write, `{"text":""}`); code != 400 || out["code"] != "bad_input" {
		t.Errorf("an empty note: %d %v", code, out)
	}
	code, out = c.do("POST", "/api/v1/alerts/"+id+"/notes", write, `{"text":"relay restarted on edge-1"}`)
	if notes, _ := out["notes"].([]any); code != 201 || len(notes) != 1 || notes[0].(map[string]any)["via"] != "api" {
		t.Fatalf("note: %d %v", code, out)
	}
	if !strings.Contains(e.Bot.Texts[len(e.Bot.Texts)-1], "claude-code left a note") {
		t.Errorf("echo: %v", e.Bot.Texts)
	}

	code, out = c.do("GET", "/api/v1/alerts/"+id+"/timeline", read, "")
	events, _ := out["events"].([]any)
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.(map[string]any)["kind"].(string))
	}
	if code != 200 || strings.Join(kinds, " ") != "started notified change note" {
		t.Errorf("timeline: %d %v", code, kinds)
	}

	code, out = c.do("GET", "/api/v1/changes?service=hub-example-com&since=1h", read, "")
	if changes, _ := out["changes"].([]any); code != 200 || len(changes) != 1 || changes[0].(map[string]any)["posted_by"] != "ci" {
		t.Errorf("changes: %d %v", code, out)
	}
	// what the senders say is marked as such, as in the evidence
	fields, _ := out["untrusted"].(map[string]any)["fields"].([]any)
	for _, f := range []string{"changes[].source", "changes[].url", "changes[].summary", "changes[].machine"} {
		if !slices.Contains(fields, any(f)) {
			t.Errorf("changes: %s is not marked untrusted: %v", f, out["untrusted"])
		}
	}
	for _, q := range []string{"since=soon", "limit=x", "limit=0"} {
		if code, out := c.do("GET", "/api/v1/changes?"+q, read, ""); code != 400 || out["code"] != "bad_input" {
			t.Errorf("%s: %d %v", q, code, out)
		}
	}
}

func TestEvidenceReturnsOnlyCurrentDesignatedMachineWithinScope(t *testing.T) {
	e, c := setup(t)
	id := testutil.GroupID()
	g, ok := e.Store.Group(id)
	if !ok {
		t.Fatal("missing alert fixture")
	}
	m := store.Machine{Name: "app-1", Address: "host.example.com:22", Added: time.Now()}
	if err := e.Store.PutMachine(m); err != nil {
		t.Fatal(err)
	}
	m, _ = e.Store.Machine(m.Name)
	g.MonitorMachines = []store.MonitorMachine{{Service: "hub-example-com", Name: m.Name, Registration: m.Registration()}}
	if err := e.Store.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/alerts/" + id + "/evidence"
	allowed := e.Token(t, "allowed", "status:read", "machines:ssh")
	outside := e.Token(t, "other-machine", "status:read", "machines:ssh")
	if _, err := e.Store.UpdateToken("other-machine", func(tok *store.Token) error {
		tok.MachineAccess = store.NamedAccess("app-2")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reader := e.Token(t, "reader", "status:read")

	code, out := c.do("GET", path, allowed, "")
	links, ok := out["designated_machines"].([]any)
	if code != 200 || !ok || len(links) != 1 {
		t.Fatalf("the authorised responder cannot discover the current designation: %d %v", code, out["designated_machines"])
	}
	link, _ := links[0].(map[string]any)
	if len(link) != 2 || link["name"] != m.Name || link["service"] != "hub-example-com" {
		t.Fatalf("wrong public designation metadata: %v", link)
	}
	for _, token := range []string{outside, reader} {
		if code, out := c.do("GET", path, token, ""); code != 200 || out["designated_machines"] != nil {
			t.Errorf("a token without SSH access could see the designation: %d %v", code, out["designated_machines"])
		}
	}

	m.Added = m.Added.Add(time.Minute)
	if err := e.Store.PutMachine(m); err != nil {
		t.Fatal(err)
	}
	if code, out := c.do("GET", path, allowed, ""); code != 200 || out["designated_machines"] != nil {
		t.Errorf("an older incident pointed to a replacement registration: %d %v", code, out["designated_machines"])
	}
}
