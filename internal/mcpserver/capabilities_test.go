package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestMCPCapabilitiesMetadataResourcesAndPrompts(t *testing.T) {
	e := testutil.New(t)
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	if _, err := cfg.Put(context.Background(), configdb.Actor{Name: "test", Via: "cli"},
		"locations", "edge-1", json.RawMessage(`{"name":"Frankfurt","region":"overseas"}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Put(context.Background(), configdb.Actor{Name: "test", Via: "cli"},
		"status_pages", "public", json.RawMessage(`{"title":"Example Status","components":[{"name":"hub","title":"Hub","services":["hub-example-com"]}]}`), ""); err != nil {
		t.Fatal(err)
	}
	pages := status.New(status.Deps{Config: cfg, Bridge: e.Bridge, Store: e.Store})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithConfig(cfg), WithStatus(pages)))
	defer srv.Close()

	// Create an owner and a delegated agent to test list_agents / get_agent parity and restrictions.
	setupCode, _, err := e.Store.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.Join(context.Background(), setupCode, "alice", "Alice", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	agent, err := e.Bridge.CreateAgent(context.Background(), cli, bridge.AgentInput{
		Name: "triage-bot", Owner: "alice", Preset: "oncall", TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	admin := connect(t, srv.URL, e.Token(t, "admin", "status:read", "accounts:write", "machines:ssh"))
	ctx := context.Background()

	// 1. Every tool exposes canonical domain/access/rest metadata.
	tools, err := admin.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Meta == nil || tool.Meta["domain"] == "" || tool.Meta["rest"] == "" || tool.Meta["access"] == "" {
			t.Errorf("tool %q missing structured domain metadata: %+v", tool.Name, tool.Meta)
		}
	}

	// 2. describe_capabilities and get_ssh_status tools.
	res, text := call(t, admin, "describe_capabilities", nil)
	if res.IsError || !strings.Contains(text, `"incidents"`) || !strings.Contains(text, `"triage_alert"`) {
		t.Fatalf("describe_capabilities: %v %s", res.IsError, text)
	}
	res, text = call(t, admin, "get_ssh_status", nil)
	if res.IsError || !strings.Contains(text, `"enabled":false`) {
		t.Fatalf("get_ssh_status: %v %s", res.IsError, text)
	}

	// 3. list_agents and get_agent work for standalone admin and refuse delegated agents.
	res, text = call(t, admin, "list_agents", map[string]any{"owner": "alice"})
	if res.IsError || !strings.Contains(text, `"triage-bot"`) {
		t.Fatalf("list_agents: %v %s", res.IsError, text)
	}
	res, text = call(t, admin, "get_agent", map[string]any{"name": "triage-bot"})
	if res.IsError || !strings.Contains(text, `"preset":"oncall"`) {
		t.Fatalf("get_agent: %v %s", res.IsError, text)
	}

	bot := connect(t, srv.URL, agent.Secret)
	if res, _ := call(t, bot, "list_agents", nil); !res.IsError {
		t.Error("delegated agent should not list agents")
	}

	// 4. Static MCP Resources and Resource Templates.
	resources, err := admin.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 6 {
		t.Fatalf("ListResources: %v %+v", err, resources)
	}
	templates, err := admin.ListResourceTemplates(ctx, nil)
	if err != nil || len(templates.ResourceTemplates) != 5 {
		t.Fatalf("ListResourceTemplates: %v %+v", err, templates)
	}
	for _, tc := range []struct {
		uri, want string
	}{
		{"beacon://capabilities", `"domains"`},
		{"beacon://overview", `"alerting"`},
		{"beacon://alerts/firing", testutil.GroupID()},
		{"beacon://silences", `"silences"`},
		{"beacon://maintenance", `"maintenance"`},
		{"beacon://alerts/" + testutil.GroupID() + "/evidence", `"alert"`},
		{"beacon://alerts/" + testutil.GroupID() + "/timeline", `"events"`},
		{"beacon://status/public", `"Example Status"`},
		{"beacon://config/locations", `"Frankfurt"`},
	} {
		out, err := admin.ReadResource(ctx, &mcp.ReadResourceParams{URI: tc.uri})
		if err != nil || len(out.Contents) != 1 || !strings.Contains(out.Contents[0].Text, tc.want) {
			t.Errorf("ReadResource(%s): err=%v out=%+v", tc.uri, err, out)
		}
	}

	// Scope checks apply to resources too.
	noStatus := connect(t, srv.URL, e.Token(t, "ssh-only", "machines:ssh"))
	if _, err := noStatus.ReadResource(ctx, &mcp.ReadResourceParams{URI: "beacon://alerts/firing"}); err == nil {
		t.Error("beacon://alerts/firing should refuse a credential without status:read")
	}
	// Capabilities resource is open to any authenticated credential.
	if out, err := noStatus.ReadResource(ctx, &mcp.ReadResourceParams{URI: "beacon://capabilities"}); err != nil || len(out.Contents) != 1 {
		t.Errorf("beacon://capabilities for ssh-only: err=%v", err)
	}

	// 5. Operational MCP Prompts.
	prompts, err := admin.ListPrompts(ctx, nil)
	if err != nil || len(prompts.Prompts) != 5 {
		t.Fatalf("ListPrompts: %v %+v", err, prompts)
	}
	for _, tc := range []struct {
		name string
		args map[string]string
		want string
	}{
		{"triage_alert", map[string]string{"alert_id": testutil.GroupID()}, "get_evidence"},
		{"verify_recovery", map[string]string{"alert_id": testutil.GroupID(), "expected_episode": "ep-1"}, "probe_now"},
		{"fleet_health", nil, "status_summary"},
		{"shift_handover", map[string]string{"hours": "12"}, "12 hours"},
		{"onboard_monitor", map[string]string{"service": "api.example.com", "url": "https://api.example.com/healthz"}, "if_version"},
	} {
		got, err := admin.GetPrompt(ctx, &mcp.GetPromptParams{Name: tc.name, Arguments: tc.args})
		if err != nil || len(got.Messages) == 0 {
			t.Fatalf("GetPrompt(%s): err=%v", tc.name, err)
		}
		txt, _ := got.Messages[0].Content.(*mcp.TextContent)
		if txt == nil || !strings.Contains(txt.Text, tc.want) {
			t.Errorf("GetPrompt(%s): missing %q in %+v", tc.name, tc.want, txt)
		}
	}
	// Missing required argument returns an error.
	if _, err := admin.GetPrompt(ctx, &mcp.GetPromptParams{Name: "triage_alert"}); err == nil {
		t.Error("triage_alert without alert_id should fail")
	}

	// Ensure store.Principal fields still match expectations when created via agent.
	if p, ok := e.Store.Authenticate(agent.Secret); !ok || p.Agent != "triage-bot" || p.Preset != "oncall" {
		t.Errorf("unexpected agent principal: %+v", p)
	}
}
