package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestIncidentOutcomeRoutes(t *testing.T) {
	e, c := setup(t)
	read := e.Token(t, "reader", "status:read")
	noRead := e.Token(t, "machine", "machines:ssh")
	group := testutil.GroupID()
	path := "/api/v1/incident-outcomes"
	if code, _ := c.do(http.MethodGet, path, noRead, ""); code != http.StatusForbidden {
		t.Fatalf("read permission: %d", code)
	}
	if code, out := c.do(http.MethodGet, path+"?limit=201", read, ""); code != http.StatusBadRequest || out["code"] != "bad_input" {
		t.Fatalf("page limit: %d %+v", code, out)
	}
	if err := e.Bridge.HandleWebhook(context.Background(), alertmanager.Webhook{GroupKey: testutil.GroupKey, Status: "resolved",
		CommonLabels: map[string]string{"service": "hub-example-com", "severity": "critical"}}); err != nil {
		t.Fatal(err)
	}
	code, out := c.do(http.MethodGet, path+"?group="+group, read, "")
	outcomes, _ := out["outcomes"].([]any)
	if code != http.StatusOK || len(outcomes) != 1 || out["untrusted"] == nil {
		t.Fatalf("list: %d %+v", code, out)
	}
	id, _ := outcomes[0].(map[string]any)["id"].(string)
	for _, tc := range []struct {
		query string
		count int
	}{
		{"severity=critical&since=7d&evidence=no_passing_check", 1},
		{"severity=critical&since=7d&evidence=passing_check", 0},
		{"severity=warning&since=7d&evidence=no_passing_check", 0},
	} {
		code, out := c.do(http.MethodGet, path+"?group="+group+"&"+tc.query, read, "")
		if page, _ := out["outcomes"].([]any); code != http.StatusOK || len(page) != tc.count {
			t.Errorf("%s: %d %+v", tc.query, code, out)
		}
	}
	if code, out := c.do(http.MethodGet, path+"?evidence=successful_repair", read, ""); code != http.StatusBadRequest || out["code"] != "bad_input" {
		t.Errorf("invalid evidence: %d %+v", code, out)
	}
	if code, out := c.do(http.MethodGet, path+"/"+id, read, ""); code != http.StatusOK ||
		out["outcome"].(map[string]any)["group"] != group || out["untrusted"] == nil {
		t.Fatalf("detail: %d %+v", code, out)
	}
	if code, out := c.do(http.MethodGet, path+"/nope", read, ""); code != http.StatusNotFound || out["code"] != "incident.not_found" {
		t.Fatalf("unknown incident: %d %+v", code, out)
	}
}
