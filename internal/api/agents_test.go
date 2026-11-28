package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// TestAgentGrantStaysWithinCaller is the REST face of the bridge rule: a credential never gives an
// agent a scope or a machine it does not hold. A refusal is 403 auth.scope, creates nothing and writes
// no audit entry. Narrowing onto a preset that gives nothing new still works.
func TestAgentGrantStaysWithinCaller(t *testing.T) {
	e, c := setup(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	people(t, e)
	narrow := e.Token(t, "narrow", "status:read", "accounts:write")

	audits := len(e.Store.AuditLog(1000))
	code, out := c.do("POST", "/api/v1/agents", narrow, `{"name":"wide","owner":"alice","preset":"operator","expires":"90d"}`)
	if code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "you cannot grant") || !strings.Contains(out["error"].(string), "you do not hold it") {
		t.Fatalf("narrow token creates an operator: %d %v", code, out)
	}
	if _, ok := e.Store.Agent("wide"); ok {
		t.Fatal("the refused agent was created")
	}
	if len(e.Store.AuditLog(1000)) != audits {
		t.Fatalf("the refusal was audited: %+v", e.Store.AuditLog(4))
	}

	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "reader", Owner: "alice", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	audits = len(e.Store.AuditLog(1000))
	code, out = c.do("PATCH", "/api/v1/agents/reader", narrow, `{"preset":"operator"}`)
	if code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "you do not hold it") {
		t.Fatalf("narrow token widens a preset: %d %v", code, out)
	}
	if a, _ := e.Store.Agent("reader"); a.Preset != "readonly" {
		t.Fatalf("the refused patch changed the agent: %+v", a)
	}
	if len(e.Store.AuditLog(1000)) != audits {
		t.Fatal("the refusal was audited")
	}

	admin, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "admin-bot", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", "accounts:write"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.Store.Agent("admin-bot")
	audits = len(e.Store.AuditLog(1000))
	code, out = c.do("PATCH", "/api/v1/agents/admin-bot", admin.Secret, `{"preset":"operator"}`)
	if code != 403 || out["code"] != "auth.scope" {
		t.Fatalf("an agent widens itself: %d %v", code, out)
	}
	if a, _ := e.Store.Agent("admin-bot"); a.Preset != before.Preset || len(a.Scopes) != len(before.Scopes) {
		t.Fatalf("the agent changed itself: %+v", a)
	}
	if len(e.Store.AuditLog(1000)) != audits {
		t.Fatal("the refusal was audited")
	}

	held := append(append([]string{}, store.RoleScopes("operator")...), "accounts:write")
	boxed := e.Token(t, "boxed", held...)
	if _, err := e.Store.UpdateToken("boxed", func(tok *store.Token) error { tok.MachineAccess = store.NamedAccess("edge-1"); return nil }); err != nil {
		t.Fatal(err)
	}
	audits = len(e.Store.AuditLog(1000))
	code, out = c.do("POST", "/api/v1/agents", boxed, `{"name":"everywhere","owner":"alice","preset":"operator","machine_access":"all"}`)
	if code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "all machines") {
		t.Fatalf("a limited caller grants every machine: %d %v", code, out)
	}
	if _, ok := e.Store.Agent("everywhere"); ok {
		t.Fatal("the refused agent was created")
	}
	if len(e.Store.AuditLog(1000)) != audits {
		t.Fatal("the refusal was audited")
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "local", Owner: "alice", Preset: "operator", Machines: store.NamedAccess("edge-1")}); err != nil {
		t.Fatal(err)
	}
	audits = len(e.Store.AuditLog(1000))
	code, out = c.do("PATCH", "/api/v1/agents/local", boxed, `{"machine_access":"all"}`)
	if code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "all machines") {
		t.Fatalf("a limited caller widens machines to all: %d %v", code, out)
	}
	if a, _ := e.Store.Agent("local"); len(a.Machines) != 1 {
		t.Fatalf("the machines changed: %+v", a)
	}
	if len(e.Store.AuditLog(1000)) != audits {
		t.Fatal("the refusal was audited")
	}

	// a preset that gives nothing the agent did not already want is not a grant
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "hand", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", "sessions:read", "accounts:write"}}); err != nil {
		t.Fatal(err)
	}
	code, out = c.do("PATCH", "/api/v1/agents/hand", narrow, `{"preset":"readonly"}`)
	if code != 200 || out["preset"] != "readonly" {
		t.Fatalf("a preset that is not wider: %d %v", code, out)
	}
	if a, _ := e.Store.Agent("hand"); a.Preset != "readonly" || len(a.Scopes) != 0 {
		t.Fatalf("moving off custom: %+v", a)
	}
}
