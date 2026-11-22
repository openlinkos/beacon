package mcpserver

// LOCKED acceptance and security tests for the MCP side of managing machines and agents' custom
// scopes. Written before the feature; the implementer must make them pass and must not edit, skip or
// delete them (checked by the coordinator's check-locked.sh).
//
// The MCP contract these tests pin, the same operations as the REST routes and through the same
// bridge and gate methods:
//
//	update_machine  {name, note?, critical?, user?}                        machines:write
//	create_agent    {name, owner, preset, scopes?, machines?, expires?}    custom scopes: accounts:write, not an agent
//	update_agent    {name, preset?, scopes?, machines?}                    custom scopes: accounts:write, not an agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func lockSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func lockMCP(t *testing.T) (*testutil.Env, string) {
	t.Helper()
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	e := testutil.New(t)
	gate, err := sshgate.New(sshgate.Deps{Store: e.Store, HostKey: lockSigner(t), CA: lockSigner(t), Dir: filepath.Join(t.TempDir(), "rec"),
		Lang: i18n.EN, Bot: e.Bot, Log: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	hk := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(lockSigner(t).PublicKey())))
	if err := e.Store.PutMachine(store.Machine{Name: "edge-1", Address: "192.0.2.10:22", User: "beacon", HostKey: hk, Note: "relay", Added: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, bridge.Actor{Name: "cli:root", Via: "cli"}, "oncall", "")
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(e.Bridge, SSH{Gate: gate}, &api.Auth{Store: e.Store}, slog.Default()))
	t.Cleanup(srv.Close)
	return e, srv.URL
}

func TestLockMCPUpdateMachine(t *testing.T) {
	e, u := lockMCP(t)
	before, _ := e.Store.Machine("edge-1")
	reader := connect(t, u, e.Token(t, "reader", "status:read", "machines:ssh"))
	if res, text := call(t, reader, "update_machine", map[string]any{"name": "edge-1", "note": "x"}); !res.IsError || !strings.Contains(text, "machines:write") {
		t.Errorf("without machines:write: %v %s", res.IsError, text)
	}
	if m, _ := e.Store.Machine("edge-1"); m.Note != "relay" {
		t.Fatal("a refused update changed the machine")
	}
	op := connect(t, u, e.Token(t, "op-script", "status:read", "machines:write"))
	if res, text := call(t, op, "update_machine", map[string]any{"name": "edge-1", "note": "moved", "critical": true}); res.IsError {
		t.Fatalf("update_machine: %s", text)
	}
	m, _ := e.Store.Machine("edge-1")
	if m.Note != "moved" || !m.Critical || m.HostKey != before.HostKey || m.User != "beacon" {
		t.Errorf("after update_machine: %+v", m)
	}
	found := false
	for _, a := range e.Store.AuditLog(100) {
		if a.Action == "machine_update" && a.Target == "edge-1" && a.Via == "mcp" {
			found = true
		}
	}
	if !found {
		t.Errorf("update_machine is not audited as MCP's: %+v", e.Store.AuditLog(5))
	}
	if res, _ := call(t, op, "update_machine", map[string]any{"name": "nope", "note": "x"}); !res.IsError {
		t.Error("an unknown machine")
	}
}

func TestLockMCPAgentCustomScopes(t *testing.T) {
	e, u := lockMCP(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "codex", Owner: "bob", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	admin, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "admin-bot", Owner: "alice", Preset: store.PresetCustom,
		Scopes: slices.Clone(store.PeopleScopes), TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	owner := connect(t, u, e.Token(t, "owner-script", store.PeopleScopes...))
	if res, text := call(t, owner, "create_agent", map[string]any{"name": "ops-bot", "owner": "alice", "preset": "custom",
		"scopes": []string{"status:read", "config:write"}}); res.IsError {
		t.Fatalf("create_agent: %s", text)
	}
	if a, ok := e.Store.Agent("ops-bot"); !ok || a.Preset != store.PresetCustom || !slices.Contains(a.Scopes, "config:write") {
		t.Errorf("ops-bot: %+v %v", a, ok)
	}
	if res, text := call(t, owner, "update_agent", map[string]any{"name": "codex", "preset": "custom", "scopes": []string{"status:read", "alerts:write"}}); res.IsError {
		t.Fatalf("update_agent: %s", text)
	}
	if a, _ := e.Store.Agent("codex"); a.Preset != store.PresetCustom || slices.Contains(a.Scopes, "machines:ssh") {
		t.Errorf("codex: %+v", a)
	}
	found := false
	for _, a := range e.Store.AuditLog(100) {
		if a.Action == "agent_update" && a.Target == "codex" && a.Via == "mcp" && strings.Contains(a.Detail, "custom") {
			found = true
		}
	}
	if !found {
		t.Errorf("update_agent not audited: %+v", e.Store.AuditLog(5))
	}

	codexBefore, _ := e.Store.Agent("codex")
	n := len(e.Store.Agents(""))
	// refused: an agent, even with accounts:write; beyond the owner's role; unknown or machines' scopes
	ab := connect(t, u, admin.Secret)
	for what, try := range map[string]func() (bool, string){
		"agent creates": func() (bool, string) {
			r, s := call(t, ab, "create_agent", map[string]any{"name": "x-1", "owner": "alice", "preset": "custom", "scopes": []string{"config:write"}})
			return r.IsError, s
		},
		"agent widens another": func() (bool, string) {
			r, s := call(t, ab, "update_agent", map[string]any{"name": "codex", "preset": "custom", "scopes": []string{"status:read", "alerts:write", "machines:ssh"}})
			return r.IsError, s
		},
		"agent changes itself": func() (bool, string) {
			r, s := call(t, ab, "update_agent", map[string]any{"name": "admin-bot", "preset": "custom", "scopes": []string{"status:read"}})
			return r.IsError, s
		},
		"beyond bob's role": func() (bool, string) {
			r, s := call(t, owner, "update_agent", map[string]any{"name": "codex", "preset": "custom", "scopes": []string{"config:write"}})
			return r.IsError, s
		},
		"unknown scope": func() (bool, string) {
			r, s := call(t, owner, "create_agent", map[string]any{"name": "x-2", "owner": "alice", "preset": "custom", "scopes": []string{"root:all"}})
			return r.IsError, s
		},
		"machine's scope": func() (bool, string) {
			r, s := call(t, owner, "create_agent", map[string]any{"name": "x-3", "owner": "alice", "preset": "custom", "scopes": []string{"probe:write"}})
			return r.IsError, s
		},
		"without accounts:write": func() (bool, string) {
			op := connect(t, u, e.Token(t, "op", "status:read", "machines:write"))
			r, s := call(t, op, "create_agent", map[string]any{"name": "x-4", "owner": "alice", "preset": "custom", "scopes": []string{"status:read"}})
			return r.IsError, s
		},
	} {
		if isErr, text := try(); !isErr {
			t.Errorf("%s was allowed: %s", what, text)
		}
	}
	if len(e.Store.Agents("")) != n {
		t.Error("a refused call made an agent")
	}
	if a, _ := e.Store.Agent("codex"); !slices.Equal(a.Scopes, codexBefore.Scopes) || a.Preset != codexBefore.Preset {
		t.Errorf("codex changed by a refused call: %+v", a)
	}
}
