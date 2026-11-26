package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

func TestCapabilitiesAndAgentReadRoutes(t *testing.T) {
	e, c := setup(t)
	people(t, e)
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	created, err := e.Bridge.CreateAgent(context.Background(), cli, bridge.AgentInput{
		Name: "oncall-bot", Owner: "alice", Preset: "oncall", Machines: []string{"edge-1"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Any authenticated credential can inspect its own capabilities.
	req, _ := http.NewRequest(http.MethodGet, c.srv.URL+"/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capabilities: %d %s", resp.StatusCode, raw)
	}
	var caps Capabilities
	if err := json.Unmarshal(raw, &caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if caps.Caller.Agent != "oncall-bot" || caps.Caller.MachineScope != "listed" || len(caps.Caller.Machines) != 1 || caps.Caller.Machines[0] != "edge-1" {
		t.Fatalf("unexpected caller capabilities: %+v", caps.Caller)
	}
	if len(caps.Domains) != 6 {
		t.Fatalf("expected 6 domains, got %d", len(caps.Domains))
	}
	if len(caps.Resources) < 9 || len(caps.Prompts) != 5 {
		t.Fatalf("expected MCP resources and 5 prompts, got %d resources %d prompts", len(caps.Resources), len(caps.Prompts))
	}
	ops := map[string]Operation{}
	for _, d := range caps.Domains {
		for _, op := range d.Operations {
			ops[op.Tool] = op
		}
	}
	if !ops["get_evidence"].Available {
		t.Error("oncall agent should have get_evidence available")
	}
	if ops["invite_user"].Available {
		t.Error("delegated agent must not have invite_user available")
	}
	if ops["list_agents"].Available {
		t.Error("delegated agent must not have list_agents available")
	}

	// Standalone admin can list and inspect agents; delegated agent cannot.
	admin := e.Token(t, "admin", "status:read", "accounts:write")
	code, out := c.do("GET", "/api/v1/agents", created.Secret, "")
	if code != http.StatusForbidden || out["code"] != "auth.scope" {
		t.Fatalf("delegated agent listing agents: %d %v", code, out)
	}
	code, out = c.do("GET", "/api/v1/agents/oncall-bot", created.Secret, "")
	if code != http.StatusForbidden || out["code"] != "auth.scope" {
		t.Fatalf("delegated agent reading agent: %d %v", code, out)
	}

	code, out = c.do("GET", "/api/v1/agents?owner=alice", admin, "")
	agents, _ := out["agents"].([]any)
	if code != http.StatusOK || len(agents) != 1 {
		t.Fatalf("admin list agents: %d %v", code, out)
	}
	code, out = c.do("GET", "/api/v1/agents/oncall-bot", admin, "")
	if code != http.StatusOK || out["name"] != "oncall-bot" || out["owner"] != "alice" {
		t.Fatalf("admin get agent: %d %v", code, out)
	}
	code, out = c.do("GET", "/api/v1/agents/missing", admin, "")
	if code != http.StatusNotFound || out["code"] != "agent.not_found" {
		t.Fatalf("missing agent: %d %v", code, out)
	}

	// machines:ssh alone can inspect /api/v1/ssh status.
	sshOnly := e.Token(t, "ssh-only", "machines:ssh")
	code, out = c.do("GET", "/api/v1/ssh", sshOnly, "")
	if code != http.StatusOK || out["enabled"] != false {
		t.Fatalf("ssh-only status: %d %v", code, out)
	}
}

func TestOpenAPIAllOperationsTagged(t *testing.T) {
	var doc struct {
		Tags []struct {
			Name string `yaml:"name"`
		} `yaml:"tags"`
		Paths map[string]map[string]struct {
			Tags []string `yaml:"tags"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, tag := range doc.Tags {
		known[tag.Name] = true
	}
	if len(known) != 6 {
		t.Fatalf("expected 6 top-level domain tags, got %d", len(known))
	}
	for path, methods := range doc.Paths {
		for method, op := range methods {
			if len(op.Tags) == 0 {
				t.Errorf("%s %s has no domain tag", strings.ToUpper(method), path)
				continue
			}
			for _, tag := range op.Tags {
				if !known[tag] {
					t.Errorf("%s %s uses undeclared tag %q", strings.ToUpper(method), path, tag)
				}
			}
		}
	}
}

func TestCapabilityCatalogCoversAllOpDefs(t *testing.T) {
	caps := BuildCapabilities(store.Principal{User: "alice", Scopes: store.PeopleScopes}, Features{
		SSH: true, Config: true, Status: true, ProbeNow: true, TeamCredentials: true, ShareLinks: true,
	})
	seen := map[string]bool{}
	for _, d := range caps.Domains {
		if d.Summary == "" || d.Workflow == "" || len(d.Operations) == 0 {
			t.Errorf("domain %q is incomplete", d.ID)
		}
		for _, op := range d.Operations {
			if seen[op.Tool] {
				t.Errorf("duplicate tool %q", op.Tool)
			}
			seen[op.Tool] = true
			if ToolMeta(op.Tool) == nil {
				t.Errorf("ToolMeta(%q) returned nil", op.Tool)
			}
		}
	}
}
