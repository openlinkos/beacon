package api

import (
	"net/http"
	"testing"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestIncidentEpisodeRoutesRetainTheOpenDenominator(t *testing.T) {
	e, c := setup(t)
	read := e.Token(t, "episode-reader", "status:read")
	noRead := e.Token(t, "episode-writer", "alerts:write")
	group := testutil.GroupID()
	path := "/api/v1/incident-episodes?severity=critical&group=" + group
	if status, _ := c.do(http.MethodGet, path, noRead, ""); status != http.StatusForbidden {
		t.Fatalf("status:read protection: %d", status)
	}
	episode := func(want string) map[string]any {
		t.Helper()
		code, body := c.do(http.MethodGet, path, read, "")
		list, _ := body["episodes"].([]any)
		if code != http.StatusOK || len(list) != 1 || body["since"] == nil || body["until"] == nil || body["retained_from"] == nil {
			t.Fatalf("episode page: %d %+v", code, body)
		}
		row := list[0].(map[string]any)
		if row["state"] != want || row["wake"] != "unknown" || row["origin"] != "unknown" {
			t.Fatalf("episode observation: %+v", row)
		}
		return row
	}
	open := episode("open")
	g, _ := e.Store.Group(group)
	if err := e.Bridge.HandleWebhook(t.Context(), alertmanager.Webhook{GroupKey: g.Key, Status: "resolved", CommonLabels: g.Common}); err != nil {
		t.Fatal(err)
	}
	closed := episode("resolved")
	if closed["episode"] != open["episode"] || closed["started_at"] != open["started_at"] || closed["outcome_id"] == nil {
		t.Fatalf("recovery changed episode identity: %+v %+v", open, closed)
	}
}
