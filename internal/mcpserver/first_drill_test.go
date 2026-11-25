package mcpserver

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestDrillHookCheckOnlyDisclosesTheAssignedHook(t *testing.T) {
	const privateDestination = "https://hooks.example.com/private-path"
	const fixtureSecret = "synthetic hook signing secret"
	e := testutil.NewWith(t, func(deps *bridge.Deps) {
		deps.Bot = nil // no critical chat in this running instance
		deps.Config.Agents = []config.Agent{{Name: "test-hook", Reporter: "token:runner", Service: "drill",
			Severities: []string{"critical"}, URL: privateDestination, Secret: fixtureSecret, Kind: "webhook"}}
	})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default()))
	defer srv.Close()
	runner := connect(t, srv.URL, e.Token(t, "runner", bridge.AgentRunScope))
	other := connect(t, srv.URL, e.Token(t, "other", bridge.AgentRunScope))
	viewer := connect(t, srv.URL, e.Token(t, "viewer", "status:read"))
	check := map[string]any{"name": "test-hook"}
	result, body := call(t, runner, "check_drill_hook", check)
	if result.IsError || strings.Contains(body, privateDestination) || strings.Contains(body, fixtureSecret) || strings.Contains(body, "token:runner") {
		t.Fatal("the read failed or exposed hook credentials or destination")
	}
	var data bridge.DrillHookCheck
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(encoded, &data) != nil || data.CriticalChat != "none" ||
		!data.SignedWebhook || !data.MatchesDrill || !data.DrillOnly {
		t.Fatalf("unexpected loaded configuration: %s (%v)", encoded, err)
	}
	result, unassigned := call(t, other, "check_drill_hook", check)
	if !result.IsError {
		t.Fatal("another reporter can check a hook assigned to a different credential")
	}
	result, missing := call(t, other, "check_drill_hook", map[string]any{"name": "absent"})
	if !result.IsError || unassigned != missing {
		t.Fatal("an unrelated reporter can distinguish missing from assigned")
	}
	result, _ = call(t, viewer, "check_drill_hook", check)
	if !result.IsError {
		t.Fatal("a token without the reporter scope can check a hook")
	}
}
