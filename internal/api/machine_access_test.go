package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// limited makes a token with scopes that names only the machines given.
func limited(t *testing.T, st *store.Store, name string, access store.MachineAccess, scopes ...string) string {
	t.Helper()
	secret, _, err := st.CreateToken(name, scopes, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateToken(name, func(tok *store.Token) error { tok.MachineAccess = access; return nil }); err != nil {
		t.Fatal(err)
	}
	return secret
}

// TestTokenDeletionStaysWithinTheCaller: a token is deleted only by a caller with access to all machines
// holding everything the token holds; an agent's token by the agent itself or a person.
func TestTokenDeletionStaysWithinTheCaller(t *testing.T) {
	e, c := setup(t)
	people(t, e) // bob / codex, an on-call agent with its token
	admin := limited(t, e.Store, "admin", store.AllAccess(), "status:read", "accounts:write", "machines:ssh", "machines:write")
	limited(t, e.Store, "ssh-only", store.AllAccess(), "status:read", "machines:ssh")
	boxed := limited(t, e.Store, "boxed", store.NamedAccess("edge-1"), "status:read", "machines:ssh", "machines:write")
	op := limited(t, e.Store, "op", store.AllAccess(), "status:read", "machines:ssh", "machines:write")
	tokenNames := func() map[string]bool {
		out := map[string]bool{}
		for _, tok := range e.Store.Tokens() {
			out[tok.Name] = true
		}
		return out
	}
	audits := len(e.Store.AuditLog(1000))
	// A caller that may delete no token is told so before any token is looked up; a refusal for one
	// token answers as a token that does not exist would, so that it says nothing about which exist.
	for _, tc := range []struct {
		who, caller, target string
		status              int
		code, why           string
	}{
		{"a credential that names its machines", boxed, "ssh-only", 403, "token.forbidden", "all machines"},
		{"a credential that names its machines, an agent's token", boxed, "codex", 403, "token.forbidden", "all machines"},
		{"a credential that names its machines, no such token", boxed, "nothing", 403, "token.forbidden", "all machines"},
		{"a token without a scope the target holds", op, "admin", 404, "token.not_found", "admin"},
		{"a token, another person's agent's token", op, "codex", 404, "token.not_found", "codex"},
		{"a token, no such token", op, "nothing", 404, "token.not_found", "nothing"},
	} {
		code, out := c.do("DELETE", "/api/v1/tokens/"+tc.target, tc.caller, "")
		if code != tc.status || out["code"] != tc.code || !strings.Contains(out["error"].(string), tc.why) {
			t.Errorf("%s: %d %v", tc.who, code, out)
		}
	}
	if names := tokenNames(); !names["ssh-only"] || !names["codex"] || !names["admin"] {
		t.Fatalf("a refused deletion deleted: %v", names)
	}
	if len(e.Store.AuditLog(1000)) != audits {
		t.Error("a refusal was audited")
	}
	// within the caller: deleted, audited
	if code, out := c.do("DELETE", "/api/v1/tokens/ssh-only", op, ""); code != 200 || out["ok"] != true {
		t.Errorf("a token within the caller: %d %v", code, out)
	}
	if code, _ := c.do("DELETE", "/api/v1/tokens/op", admin, ""); code != 200 {
		t.Errorf("an admin deletes a weaker token: %d", code)
	}
	// an agent's token: by an admin who manages agents
	if code, out := c.do("DELETE", "/api/v1/tokens/codex", admin, ""); code != 200 {
		t.Errorf("an admin deletes an agent's token: %d %v", code, out)
	}
	if names := tokenNames(); names["ssh-only"] || names["op"] || names["codex"] {
		t.Errorf("after the deletions: %v", names)
	}
}

// TestMachineWritesRefusedThroughTheAPI: the API answers machine.forbidden to a credential that names its
// machines, for the machine it names as for the others.
func TestMachineWritesRefusedThroughTheAPI(t *testing.T) {
	e, c, _ := lockSetup(t)
	boxed := limited(t, e.Store, "boxed", store.NamedAccess("edge-1"), "status:read", "machines:ssh", "machines:write")
	before, _ := e.Store.Machine("edge-1")
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/v1/machines", `{"name": "edge-1", "address": "198.51.100.7", "replace": true}`},
		{"POST", "/api/v1/machines", `{"name": "edge-9", "address": "198.51.100.7"}`},
		{"PATCH", "/api/v1/machines/edge-1", `{"note": "x"}`},
		{"PATCH", "/api/v1/machines/edge-1", `{"user": "root"}`},
		{"PUT", "/api/v1/machines/edge-1/connector", `{"key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIc2hpaMVmZcVbUQM2LPRdl6obEbBmEd1+GQBUn5R4Re x"}`},
		{"DELETE", "/api/v1/machines/edge-1", ""},
	} {
		if code, out := c.do(r.method, r.path, boxed, r.body); code != 403 || out["code"] != "machine.forbidden" {
			t.Errorf("%s %s %s: %d %v", r.method, r.path, r.body, code, out)
		}
	}
	if m, ok := e.Store.Machine("edge-1"); !ok || m != before {
		t.Fatalf("a refused write changed the machine: %+v", m)
	}
	if _, ok := e.Store.Machine("edge-9"); ok {
		t.Fatal("a refused machine was added")
	}
}

// TestMachineAccessIsShown: whoami, the agents and the capabilities say which machines a credential
// reaches, never an empty value that could be read as all.
func TestMachineAccessIsShown(t *testing.T) {
	e, c := setup(t)
	people(t, e)
	if err := e.Store.PutMachine(store.Machine{Name: "ca", Address: "ca.example.com:22", User: "beacon", Critical: true}); err != nil {
		t.Fatal(err)
	}
	none := limited(t, e.Store, "none", store.NamedAccess(), "status:read", "machines:ssh")
	all := limited(t, e.Store, "all", store.AllAccess("ca"), "status:read", "machines:ssh")
	code, out := c.do("GET", "/api/v1/token", none, "")
	if code != 200 || out["machine_scope"] != "none" || out["machine_access"] != "none" || out["machine_summary"] != "no machines" {
		t.Errorf("whoami for no machines: %d %v", code, out)
	}
	code, out = c.do("GET", "/api/v1/token", all, "")
	if code != 200 || out["machine_scope"] != "all" || out["machine_access"] != "all" ||
		!strings.Contains(out["machine_summary"].(string), "including machines added later") || !strings.Contains(out["machine_summary"].(string), "ca") {
		t.Errorf("whoami for all: %d %v", code, out)
	}
	code, out = c.do("GET", "/api/v1/capabilities", all, "")
	caller, _ := out["caller"].(map[string]any)
	if code != 200 || caller["machine_access"] != "all" || caller["machine_scope"] != "all" {
		t.Errorf("capabilities: %d %v", code, caller)
	}
	admin := e.Token(t, "admin", "status:read", "accounts:write")
	if _, err := e.Bridge.CreateAgent(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, bridge.AgentInput{Name: "plain", Owner: "alice", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	code, out = c.do("GET", "/api/v1/agents/plain", admin, "")
	if code != 200 || out["machine_access"] != "none" || out["machine_summary"] != "no machines" {
		t.Errorf("an agent given no machines: %d %v", code, out)
	}
}
