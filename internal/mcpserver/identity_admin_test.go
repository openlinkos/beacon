package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

func TestMCPAccountToolsRejectAgentAdministrator(t *testing.T) {
	e, endpoint := lockMCP(t)
	agent, err := e.Bridge.CreateAgent(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, bridge.AgentInput{
		Name: "legacy-admin-agent", Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"accounts:write"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := connect(t, endpoint, agent.Secret)
	if res, msg := call(t, client, "invite_user", map[string]any{"role": "viewer"}); !res.IsError || !strings.Contains(msg, "standalone token") {
		t.Error("a delegated agent could use an account administration tool")
	}
}
