package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

func TestAccountRoutesRejectAgentAdministrator(t *testing.T) {
	e, c, _ := lockSetup(t)
	agent, err := e.Bridge.CreateAgent(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, bridge.AgentInput{
		Name: "legacy-admin-agent", Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"accounts:write"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct{ Method, Path, Body string }{
		{http.MethodGet, "/api/v1/users", ""},
		{http.MethodPost, "/api/v1/users/invitations", `{"role":"viewer"}`},
		{http.MethodPatch, "/api/v1/users/alice", `{"display_name":"New name"}`},
		{http.MethodPost, "/api/v1/users/alice/reset", ""},
	} {
		if code, out := c.do(req.Method, req.Path, agent.Secret, req.Body); code != http.StatusForbidden || out["code"] != "auth.scope" {
			t.Errorf("%s %s: want forbidden, got %d (%v)", req.Method, req.Path, code, out["code"])
		}
	}
}
