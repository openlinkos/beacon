package api

// LOCKED acceptance and security tests for the REST side of managing machines and agents' custom
// scopes. Written before the feature; the implementer must make them pass and must not edit, skip or
// delete them (checked by the coordinator's check-locked.sh).
//
// The REST contract these tests pin:
//
//	PATCH /api/v1/machines/{name}   machines:write; {"note", "critical", "user"} (each optional); 200 with the machine;
//	                                the host key and address are kept; audited as machine_update
//	POST  /api/v1/agents            {"name", "owner", "preset", "scopes", "machines", "expires"}; 201 with the agent
//	                                and, when "expires" is given, its token's "secret"; custom scopes need accounts:write
//	PATCH /api/v1/agents/{name}     {"preset", "scopes", "machines"}; 200 with the agent; audited as agent_update
//
// Custom scopes: only a caller with accounts:write that is not an agent (a person's session, a legacy
// token, the command line) may set them; never beyond the agent's owner's role, never beyond what the
// caller itself may do, only scopes of people and agents (store.PeopleScopes).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
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

// lockSetup is the API with the SSH channel and the configuration, alice (owner), bob (on call) and
// his agent codex, and one machine, edge-1.
func lockSetup(t *testing.T) (*testutil.Env, client, string) {
	t.Helper()
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
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, SSH: gate, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Config: cfg}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	agent := people(t, e)
	return e, client{t, srv}, agent
}

func lockAudited(st *store.Store, action, target string, parts ...string) bool {
	for _, a := range st.AuditLog(100000) {
		if a.Action != action || a.Target != target {
			continue
		}
		ok := true
		for _, p := range parts {
			if !strings.Contains(a.Detail, p) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestLockAPIMachineUpdate(t *testing.T) {
	e, c, agent := lockSetup(t)
	before, _ := e.Store.Machine("edge-1")
	op := e.Token(t, "op-script", "status:read", "machines:write")

	// refused: no token, no machines:write (a reader, an on-call agent), and nothing changes
	if code, _ := c.do("PATCH", "/api/v1/machines/edge-1", "", `{"note": "x"}`); code != 401 {
		t.Errorf("no token: %d", code)
	}
	for name, tok := range map[string]string{"reader": e.Token(t, "reader", "status:read", "machines:ssh"), "agent": agent} {
		if code, out := c.do("PATCH", "/api/v1/machines/edge-1", tok, `{"note": "x"}`); code != 403 || out["code"] != "auth.scope" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if m, _ := e.Store.Machine("edge-1"); m.Note != "relay" {
		t.Fatal("a refused update changed the machine")
	}

	// a token marks no machine critical and changes no account: an owner or a person does
	for _, body := range []string{`{"note": "the relay, moved", "critical": true}`, `{"note": "the relay, moved", "user": "ops"}`} {
		if code, out := c.do("PATCH", "/api/v1/machines/edge-1", op, body); code != 403 || out["code"] != "machine.forbidden" {
			t.Errorf("%s by a token: %d %v", body, code, out)
		}
	}
	if m, _ := e.Store.Machine("edge-1"); m != before {
		t.Fatalf("a refused update changed the machine: %+v", m)
	}
	code, out := c.do("PATCH", "/api/v1/machines/edge-1", op, `{"note": "the relay, moved"}`)
	if code != 200 {
		t.Fatalf("update: %d %v", code, out)
	}
	m, _ := e.Store.Machine("edge-1")
	if m.Note != "the relay, moved" || m.Critical || m.User != "beacon" || m.HostKey != before.HostKey || m.Address != before.Address {
		t.Errorf("after the update: %+v", m)
	}
	if !lockAudited(e.Store, "machine_update", "edge-1", "moved") {
		t.Errorf("not audited: %+v", e.Store.AuditLog(5))
	}
	if !slices.ContainsFunc(e.Bot.Texts, func(s string) bool { return strings.Contains(s, "op-script") && strings.Contains(s, "edge-1") }) {
		t.Errorf("not echoed: %v", e.Bot.Texts)
	}
	// only the fields given change
	if code, _ := c.do("PATCH", "/api/v1/machines/edge-1", op, `{"critical": false, "user": "beacon"}`); code != 200 {
		t.Errorf("partial update: %d", code)
	}
	if m, _ := e.Store.Machine("edge-1"); m.Critical || m.Note != "the relay, moved" || m.User != "beacon" {
		t.Errorf("a partial update touched other fields: %+v", m)
	}
	if code, out := c.do("PATCH", "/api/v1/machines/edge-1", op, `{"user": "Root; rm"}`); code != 400 || out["code"] != "bad_input" {
		t.Errorf("a bad account: %d %v", code, out)
	}
	if code, out := c.do("PATCH", "/api/v1/machines/edge-1", op, `{"host_key": "ssh-ed25519 AAAA"}`); code != 400 {
		t.Errorf("the host key is not changed by an update: %d %v", code, out)
	}
	if code, out := c.do("PATCH", "/api/v1/machines/nope", op, `{"note": "x"}`); code != 404 || out["code"] != "machine.not_found" {
		t.Errorf("unknown machine: %d %v", code, out)
	}
}

func TestLockAPIAgentCustomScopes(t *testing.T) {
	e, c, agent := lockSetup(t)
	ctx := context.Background()
	owner := e.Token(t, "owner-script", store.PeopleScopes...)

	// the owner's script makes a custom agent of alice's with config:write, which then changes the
	// configuration; back on a preset, it may not
	code, out := c.do("POST", "/api/v1/agents", owner, `{"name": "ops-bot", "owner": "alice", "preset": "custom", "scopes": ["status:read", "config:write"], "expires": "90d"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	secret, _ := out["secret"].(string)
	if !strings.HasPrefix(secret, "lbt_") {
		t.Fatalf("no token in %v", out)
	}
	if !lockAudited(e.Store, "agent_create", "ops-bot", "custom") {
		t.Errorf("create not audited: %+v", e.Store.AuditLog(5))
	}
	put := `{"max_age": "26h", "severity": "warning"}`
	if code, out := c.do("PUT", "/api/v1/config/heartbeats/nas", secret, put); code >= 300 {
		t.Errorf("the custom agent changes the configuration: %d %v", code, out)
	}
	if code, out := c.do("PATCH", "/api/v1/agents/ops-bot", owner, `{"preset": "readonly"}`); code != 200 {
		t.Fatalf("back to a preset: %d %v", code, out)
	}
	if code, _ := c.do("PUT", "/api/v1/config/heartbeats/nas", secret, put); code != 403 {
		t.Errorf("after the preset, the configuration: %d", code)
	}
	// and to custom again, with every change audited with what changed
	if code, out := c.do("PATCH", "/api/v1/agents/ops-bot", owner, `{"preset": "custom", "scopes": ["status:read", "config:write", "machines:write"]}`); code != 200 {
		t.Fatalf("to custom: %d %v", code, out)
	}
	if !lockAudited(e.Store, "agent_update", "ops-bot", "readonly") || !lockAudited(e.Store, "agent_update", "ops-bot", "custom", "machines:write") {
		t.Errorf("updates not audited with before and after: %+v", e.Store.AuditLog(5))
	}
	if !slices.ContainsFunc(e.Bot.Texts, func(s string) bool { return strings.Contains(s, "owner-script") && strings.Contains(s, "ops-bot") }) {
		t.Errorf("not echoed: %v", e.Bot.Texts)
	}

	_, _ = agent, ctx
}

func lockSorted(v []string) []string {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}

func TestLockAPIAgentCustomScopesRefused(t *testing.T) {
	e, c, agent := lockSetup(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	owner := e.Token(t, "owner-script", store.PeopleScopes...)
	admin, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "admin-bot", Owner: "alice", Preset: store.PresetCustom,
		Scopes: slices.Clone(store.PeopleScopes), TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	narrowAdmin := e.Token(t, "narrow-admin", "status:read", "accounts:write")
	legacy := e.Token(t, "grokbot-oncall", "status:read", "alerts:write", "machines:ssh", "sessions:read")
	codexBefore, _ := e.Store.Agent("codex")
	agents := len(e.Store.Agents(""))
	audits := len(e.Store.AuditLog(100000))

	refused := func(what, method, path, tok, body string) {
		t.Helper()
		if code, out := c.do(method, path, tok, body); code < 400 || code == 404 && method == "POST" {
			t.Errorf("%s: %d %v", what, code, out)
		}
	}
	// no token, or an agent's token even with accounts:write
	refused("no token", "POST", "/api/v1/agents", "", `{"name": "x-1", "owner": "alice", "preset": "custom", "scopes": ["status:read"]}`)
	refused("bob's agent makes one", "POST", "/api/v1/agents", agent, `{"name": "x-2", "owner": "bob", "preset": "custom", "scopes": ["status:read"]}`)
	refused("bob's agent widens itself", "PATCH", "/api/v1/agents/codex", agent, `{"preset": "custom", "scopes": ["status:read", "alerts:write", "machines:write"]}`)
	refused("admin-bot makes one", "POST", "/api/v1/agents", admin.Secret, `{"name": "x-3", "owner": "alice", "preset": "custom", "scopes": ["config:write"]}`)
	refused("admin-bot widens codex", "PATCH", "/api/v1/agents/codex", admin.Secret, `{"preset": "custom", "scopes": ["status:read", "alerts:write"]}`)
	refused("admin-bot changes itself", "PATCH", "/api/v1/agents/admin-bot", admin.Secret, `{"preset": "custom", "scopes": ["status:read"]}`)
	// beyond the owner's role, unknown, machines' scopes, none
	refused("beyond bob's role", "POST", "/api/v1/agents", owner, `{"name": "x-4", "owner": "bob", "preset": "custom", "scopes": ["status:read", "config:write"]}`)
	refused("beyond bob's role (update)", "PATCH", "/api/v1/agents/codex", owner, `{"preset": "custom", "scopes": ["machines:write"]}`)
	refused("unknown scope", "POST", "/api/v1/agents", owner, `{"name": "x-5", "owner": "alice", "preset": "custom", "scopes": ["root:all"]}`)
	refused("machine's scope", "POST", "/api/v1/agents", owner, `{"name": "x-6", "owner": "alice", "preset": "custom", "scopes": ["probe:write"]}`)
	refused("machine's scope (update)", "PATCH", "/api/v1/agents/codex", owner, `{"preset": "custom", "scopes": ["host:write"]}`)
	refused("no scope", "POST", "/api/v1/agents", owner, `{"name": "x-7", "owner": "alice", "preset": "custom", "scopes": []}`)
	// beyond what the caller may do itself
	refused("beyond the granter", "POST", "/api/v1/agents", narrowAdmin, `{"name": "x-8", "owner": "alice", "preset": "custom", "scopes": ["status:read", "config:write"]}`)
	// without accounts:write
	refused("without accounts:write", "POST", "/api/v1/agents", e.Token(t, "op", "status:read", "machines:write"), `{"name": "x-9", "owner": "alice", "preset": "custom", "scopes": ["status:read"]}`)
	// a legacy token is no agent, and widens nothing
	if code, _ := c.do("PATCH", "/api/v1/agents/grokbot-oncall", owner, `{"preset": "custom", "scopes": ["config:write"]}`); code != 404 {
		t.Errorf("a legacy token as an agent: %d", code)
	}
	refused("the legacy token widens itself", "PATCH", "/api/v1/agents/grokbot-oncall", legacy, `{"preset": "custom", "scopes": ["config:write"]}`)
	if p, _ := e.Store.Authenticate(legacy); p.Has("config:write") || p.Has("accounts:write") {
		t.Errorf("the legacy token was widened: %v", p.Scopes)
	}
	if code, _ := c.do("PUT", "/api/v1/config/heartbeats/nas", legacy, `{"max_age": "26h", "severity": "warning"}`); code != 403 {
		t.Errorf("the legacy token changes the configuration: %d", code)
	}

	if n := len(e.Store.Agents("")); n != agents {
		t.Errorf("refused requests made agents: %d, was %d", n, agents)
	}
	if a, _ := e.Store.Agent("codex"); a.Preset != codexBefore.Preset || !slices.Equal(a.Scopes, codexBefore.Scopes) {
		t.Errorf("codex changed: %+v", a)
	}
	if a, _ := e.Store.Agent("admin-bot"); !slices.Equal(lockSorted(a.Scopes), lockSorted(store.PeopleScopes)) {
		t.Errorf("admin-bot changed: %+v", a)
	}
	for _, a := range e.Store.AuditLog(100000)[:len(e.Store.AuditLog(100000))-audits] {
		if a.Action == "agent_update" || a.Action == "agent_create" {
			t.Errorf("a refused request was audited: %+v", a)
		}
	}
}

func TestLockAPIRoutesListed(t *testing.T) {
	for _, r := range []string{"PATCH /api/v1/machines/{name}", "POST /api/v1/agents", "PATCH /api/v1/agents/{name}"} {
		if !slices.Contains(Routes, r) {
			t.Errorf("%s is not in Routes (and so not checked against openapi.yaml)", r)
		}
	}
}
