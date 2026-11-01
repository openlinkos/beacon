package api

import (
	"context"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestEvidenceRoutes(t *testing.T) {
	e, c := setup(t)
	read := e.Token(t, "reader", "status:read")
	write := e.Token(t, "claude-code", "status:read", "alerts:write")
	id := testutil.GroupID()
	if _, err := e.Bridge.RecordChange(context.Background(), store.Change{Source: "ci", Kind: "deploy", Service: "hub-linkos-io", Summary: "hub v2"}, "ci"); err != nil {
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

	code, out = c.do("GET", "/api/v1/changes?service=hub-linkos-io&since=1h", read, "")
	if changes, _ := out["changes"].([]any); code != 200 || len(changes) != 1 || changes[0].(map[string]any)["posted_by"] != "ci" {
		t.Errorf("changes: %d %v", code, out)
	}
	for _, q := range []string{"since=soon", "limit=x", "limit=0"} {
		if code, out := c.do("GET", "/api/v1/changes?"+q, read, ""); code != 400 || out["code"] != "bad_input" {
			t.Errorf("%s: %d %v", q, code, out)
		}
	}
}
