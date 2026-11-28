package bridge_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestMain(m *testing.M) {
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

func key(t *testing.T) (string, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), ssh.FingerprintSHA256(k)
}

// TestPeopleOperations: who may act on whose agents, what reaches the audit log and the chat, and that
// a change that ends access rechecks the SSH channel.
func TestPeopleOperations(t *testing.T) {
	rechecks := 0
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Recheck = func() int { rechecks++; return 0 } })
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, cli, "oncall", "")
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	bob := bridge.Actor{Name: "bob", Via: "console", Key: "user:bob"}
	carol := bridge.Actor{Name: "carol", Via: "console", Key: "user:carol"}

	// a key that is a machine's connector key: no agent half made
	line, fp := key(t)
	if err := e.Store.PutMachine(store.Machine{Name: "nat-box", Address: store.ConnectorAddress, User: "beacon", ConnectorKey: line, ConnectorFingerprint: fp}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "ci", SSHKeys: []string{line}, TTL: time.Hour}); !errors.Is(err, bridge.ErrBadInput) {
		t.Errorf("an agent with a connector key: %v", err)
	}
	if _, ok := e.Store.Agent("ci"); ok {
		t.Error("an agent was half made")
	}

	// bob makes his own agent; the owner defaults to him and the preset to his role's strongest
	out, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "ci", TTL: 30 * 24 * time.Hour})
	if err != nil || out.Agent.Owner != "bob" || out.Agent.Preset != "oncall" || out.Secret == "" {
		t.Fatalf("bob's agent: %+v %v", out, err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "x2", Owner: "alice", TTL: time.Hour}); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("bob makes alice's agent: %v", err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "x3", Preset: store.PresetCustom, Scopes: []string{"status:read"}, TTL: time.Hour}); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("custom scopes from the console: %v", err)
	}
	// carol is nobody: she may not touch bob's agent
	if err := e.Bridge.DeleteAgent(ctx, carol, "ci"); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("carol deletes bob's agent: %v", err)
	}
	if _, err := e.Bridge.Agents(carol, ""); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an unknown principal lists agents: %v", err)
	}
	// an agent's own token adds its own key, marked with that token
	p, _ := e.Store.Authenticate(out.Secret)
	agent := bridge.ActorOf(p, "mcp")
	line, fp = key(t)
	k, err := e.Bridge.AddSSHKey(ctx, agent, "", line+" laptop", "")
	if err != nil || k.Comment != "laptop" || k.Via != "token:ci" {
		t.Fatalf("the agent's own key: %+v %v", k, err)
	}
	if _, err := e.Bridge.AddSSHKey(ctx, agent, "other", line, ""); err == nil {
		t.Error("an agent adds a key to another agent")
	}
	// revoking the token rechecks the channel and takes the key with it
	before := rechecks
	if err := e.Bridge.RevokeAgentToken(ctx, bob, "ci"); err != nil {
		t.Fatal(err)
	}
	if rechecks == before {
		t.Error("revoking did not recheck the SSH channel")
	}
	if a, _ := e.Store.Agent("ci"); len(a.SSHKeys) != 0 {
		t.Errorf("the key outlives its token: %+v", a.SSHKeys)
	}
	_ = fp
	// demoting bob rechecks too
	before = rechecks
	role := "viewer"
	if _, err := e.Bridge.UpdateUser(ctx, cli, "bob", bridge.UserChange{Role: &role}); err != nil {
		t.Fatal(err)
	}
	if rechecks == before {
		t.Error("a demotion did not recheck the SSH channel")
	}

	// the audit log and the chat name people and their agents
	found := map[string]bool{}
	for _, en := range e.Store.AuditLog(50) {
		found[en.Actor+" "+en.Action] = true
	}
	for _, want := range []string{"bob agent_create", "bob agent_token", "bob / ci ssh_key_add", "bob revoke_token", "cli:root user_role", "cli:root user_invite", "alice setup", "bob user_join"} {
		if !found[want] {
			t.Errorf("no %q in the audit log: %v", want, found)
		}
	}
	chat := strings.Join(e.Bot.Texts, "\n")
	for _, want := range []string{"bob made agent ci for bob (On call) via the console", "bob / ci added SSH key", "bob revoked token ci via the console", "cli:root changed the role of bob to Viewer via the command line"} {
		if !strings.Contains(chat, want) {
			t.Errorf("the chat lacks %q:\n%s", want, chat)
		}
	}
	if strings.Contains(chat, out.Secret) || strings.Contains(chat, "code=") {
		t.Error("a secret or a link reached the chat")
	}
}

// TestCustomScopes: an owner (and the command line) may set custom scopes within the owner's role;
// an agent may not, even with accounts:write; a refused change writes nothing and is not audited.
func TestCustomScopes(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, cli, "oncall", "")
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "ops", Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"config:write", "status:read"}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.Store.Agent("ops"); !slices.Equal(a.Scopes, []string{"config:write", "status:read"}) {
		t.Fatalf("stored scopes: %+v", a.Scopes)
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "too", Owner: "bob", Preset: store.PresetCustom, Scopes: []string{"config:write"}}); !errors.Is(err, bridge.ErrBadInput) {
		t.Fatalf("beyond the owner's role: %v", err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "none", Owner: "alice", Preset: store.PresetCustom}); !errors.Is(err, bridge.ErrBadInput) {
		t.Fatalf("no scope: %v", err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "probe", Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"probe:write"}}); !errors.Is(err, bridge.ErrBadInput) {
		t.Fatalf("a machine's scope: %v", err)
	}

	admin, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "admin-bot", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", "accounts:write"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.Authenticate(admin.Secret)
	agent := bridge.ActorOf(p, "api")
	if _, err := e.Bridge.UpdateAgent(ctx, agent, "ops", bridge.AgentUpdate{Scopes: []string{"status:read"}}); !errors.Is(err, bridge.ErrForbidden) {
		t.Fatalf("an agent changes scopes: %v", err)
	}
	machines := store.NamedAccess("edge-1")
	if _, err := e.Bridge.UpdateAgent(ctx, agent, "ops", bridge.AgentUpdate{Machines: &machines}); !errors.Is(err, bridge.ErrForbidden) {
		t.Fatalf("an agent changed another agent's machines: %v", err)
	}
	if a, _ := e.Store.Agent("ops"); !slices.Equal(a.Scopes, []string{"config:write", "status:read"}) || len(a.Machines) != 0 {
		t.Fatalf("a refused edit changed the agent: %+v", a)
	}

	secret := e.Token(t, "owner-script", store.PeopleScopes...)
	op, _ := e.Store.Authenticate(secret)
	owner := bridge.ActorOf(op, "api")
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "codex", Owner: "bob", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	preset := store.PresetCustom
	if _, err := e.Bridge.UpdateAgent(ctx, owner, "codex", bridge.AgentUpdate{Preset: &preset, Scopes: []string{"alerts:write", "status:read"}}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range e.Store.AuditLog(50) {
		if a.Action == "agent_update" && a.Target == "codex" && strings.Contains(a.Detail, "oncall") && strings.Contains(a.Detail, "custom") && strings.Contains(a.Detail, "alerts:write") {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit: %+v", e.Store.AuditLog(5))
	}
	narrow := e.Token(t, "narrow", "status:read", "accounts:write")
	np, _ := e.Store.Authenticate(narrow)
	if _, err := e.Bridge.CreateAgent(ctx, bridge.ActorOf(np, "api"), bridge.AgentInput{Name: "wide", Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"status:read", "config:write"}}); !errors.Is(err, bridge.ErrForbidden) {
		t.Fatalf("beyond the caller: %v", err)
	}
	before, _ := e.Store.Agent("codex")
	audits := len(e.Store.AuditLog(1000))
	if _, err := e.Bridge.UpdateAgent(ctx, agent, "codex", bridge.AgentUpdate{Preset: &preset, Scopes: []string{"status:read", "machines:ssh"}}); !errors.Is(err, bridge.ErrForbidden) {
		t.Fatalf("an agent sets scopes: %v", err)
	}
	if a, _ := e.Store.Agent("codex"); a.Preset != before.Preset || !slices.Equal(a.Scopes, before.Scopes) {
		t.Fatalf("a refused update changed codex: %+v", a)
	}
	if len(e.Store.AuditLog(1000)) != audits {
		t.Fatal("a refused update was audited")
	}
	empty := []string{}
	if _, err := e.Bridge.UpdateAgent(ctx, owner, "ops", bridge.AgentUpdate{Scopes: empty}); !errors.Is(err, bridge.ErrBadInput) {
		t.Fatalf("empty scopes: %v", err)
	}
}

// quiet reports that a refusal wrote no audit entry and said nothing in the chat.
func quiet(t *testing.T, e *testutil.Env, audits, chats int) {
	t.Helper()
	if got := len(e.Store.AuditLog(1000)); got != audits {
		t.Fatalf("a refusal was audited: %d entries, was %d; %+v", got, audits, e.Store.AuditLog(6))
	}
	if got := len(e.Bot.Texts); got != chats {
		t.Fatalf("a refusal reached the chat: %v", e.Bot.Texts[chats:])
	}
}

func sameAgent(t *testing.T, e *testutil.Env, name string, before store.Agent) {
	t.Helper()
	a, ok := e.Store.Agent(name)
	if !ok || a.Preset != before.Preset || !slices.Equal(a.Scopes, before.Scopes) || !slices.Equal(a.Machines, before.Machines) {
		t.Fatalf("%s changed: before %+v after %+v", name, before, a)
	}
}

// TestGrantWithinCaller: a caller never gives an agent a scope or a machine it does not hold. A named
// preset is held to the same ceiling as custom scopes. Narrowing, and an owner signed in to the
// console, keep working. A refusal changes nothing and is not audited.
func TestGrantWithinCaller(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	for _, m := range []string{"edge-1", "edge-2"} {
		if err := e.Store.PutMachine(store.Machine{Name: m, Address: m + ".example.com:22", User: "beacon"}); err != nil {
			t.Fatal(err)
		}
	}
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, cli, "oncall", "")
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	narrowP, _ := e.Store.Authenticate(e.Token(t, "narrow", "status:read", "accounts:write"))
	narrow := bridge.ActorOf(narrowP, "api")

	// a narrow legacy token cannot make an operator agent, and nothing is written
	audits, chats := len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	if _, err := e.Bridge.CreateAgent(ctx, narrow, bridge.AgentInput{Name: "wide", Owner: "alice", Preset: "operator", TTL: time.Hour}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "you cannot grant") || !strings.Contains(err.Error(), "you do not hold it") {
		t.Fatalf("narrow token makes an operator: %v", err)
	}
	if _, ok := e.Store.Agent("wide"); ok {
		t.Fatal("the refused agent was created")
	}
	quiet(t, e, audits, chats)

	// nor turn a read-only agent into an operator
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "reader", Owner: "alice", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Store.Agent("reader")
	audits, chats = len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	op := "operator"
	if _, err := e.Bridge.UpdateAgent(ctx, narrow, "reader", bridge.AgentUpdate{Preset: &op}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "you do not hold it") {
		t.Fatalf("narrow token widens a preset: %v", err)
	}
	sameAgent(t, e, "reader", before)
	quiet(t, e, audits, chats)

	// an agent that holds accounts:write cannot put itself on a wider preset
	admin, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "admin-bot", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", "accounts:write"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ap, _ := e.Store.Authenticate(admin.Secret)
	agent := bridge.ActorOf(ap, "api")
	before, _ = e.Store.Agent("admin-bot")
	audits, chats = len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	if _, err := e.Bridge.UpdateAgent(ctx, agent, "admin-bot", bridge.AgentUpdate{Preset: &op}); !errors.Is(err, bridge.ErrForbidden) {
		t.Fatalf("an agent widens itself: %v", err)
	}
	sameAgent(t, e, "admin-bot", before)
	quiet(t, e, audits, chats)

	// a caller limited to some machines cannot hand out every machine, nor one it does not reach
	held := append(append([]string{}, store.RoleScopes("operator")...), "accounts:write")
	secret, _, err := e.Store.CreateToken("boxed", held, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.UpdateToken("boxed", func(tok *store.Token) error { tok.MachineAccess = store.NamedAccess("edge-1"); return nil }); err != nil {
		t.Fatal(err)
	}
	bp, _ := e.Store.Authenticate(secret)
	boxed := bridge.ActorOf(bp, "api")
	audits, chats = len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	if _, err := e.Bridge.CreateAgent(ctx, boxed, bridge.AgentInput{Name: "everywhere", Owner: "alice", Preset: "operator", Machines: store.AllAccess()}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "all machines") {
		t.Fatalf("a limited caller grants every machine: %v", err)
	}
	if _, ok := e.Store.Agent("everywhere"); ok {
		t.Fatal("the refused agent was created")
	}
	outside := store.NamedAccess("edge-2")
	if _, err := e.Bridge.CreateAgent(ctx, boxed, bridge.AgentInput{Name: "elsewhere", Owner: "alice", Preset: "operator", Machines: outside}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "edge-2") {
		t.Fatalf("a limited caller grants a machine it does not reach: %v", err)
	}
	quiet(t, e, audits, chats)
	// the machines it does reach are allowed
	if _, err := e.Bridge.CreateAgent(ctx, boxed, bridge.AgentInput{Name: "local", Owner: "alice", Preset: "operator", Machines: store.NamedAccess("edge-1")}); err != nil {
		t.Fatalf("a machine the caller reaches: %v", err)
	}
	// and it cannot later open that agent onto every machine
	before, _ = e.Store.Agent("local")
	audits, chats = len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	all := store.AllAccess()
	if _, err := e.Bridge.UpdateAgent(ctx, boxed, "local", bridge.AgentUpdate{Machines: &all}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "all machines") {
		t.Fatalf("a limited caller widens machines to all: %v", err)
	}
	sameAgent(t, e, "local", before)
	quiet(t, e, audits, chats)
	// widening the preset of an agent that already reaches every machine is the same refusal: the
	// result would still reach machines the caller does not
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "open", Owner: "alice", Preset: "readonly", Machines: store.AllAccess()}); err != nil {
		t.Fatal(err)
	}
	before, _ = e.Store.Agent("open")
	audits, chats = len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	if _, err := e.Bridge.UpdateAgent(ctx, boxed, "open", bridge.AgentUpdate{Preset: &op}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "all machines") {
		t.Fatalf("widening a preset while the agent reaches every machine: %v", err)
	}
	sameAgent(t, e, "open", before)
	quiet(t, e, audits, chats)
	// custom scopes obey the machines rule too
	audits, chats = len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	if _, err := e.Bridge.CreateAgent(ctx, boxed, bridge.AgentInput{Name: "custom-all", Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"status:read", "accounts:write"}, Machines: store.AllAccess()}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "all machines") {
		t.Fatalf("custom scopes with every machine: %v", err)
	}
	quiet(t, e, audits, chats)

	// narrowing stays allowed: a preset that gives nothing the agent did not already want, and a
	// shorter machine list, even when the caller does not hold the scopes that remain
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "hand", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", "sessions:read", "accounts:write"}, Machines: store.NamedAccess("edge-1", "edge-2")}); err != nil {
		t.Fatal(err)
	}
	ro := "readonly"
	only := store.NamedAccess("edge-2")
	if _, err := e.Bridge.UpdateAgent(ctx, narrow, "hand", bridge.AgentUpdate{Preset: &ro, Machines: &only}); err != nil {
		t.Fatalf("a preset that is not wider, and fewer machines: %v", err)
	}
	if a, _ := e.Store.Agent("hand"); a.Preset != "readonly" || len(a.Scopes) != 0 || !slices.Equal(a.Machines, []string{"edge-2"}) {
		t.Fatalf("the narrowed agent: %+v", a)
	}

	// an owner signed in to the console still sets any preset their role allows, and every machine
	alice, ok := e.Store.AccountPrincipal("alice")
	if !ok {
		t.Fatal("alice")
	}
	console := bridge.ActorOf(alice, "console")
	for _, preset := range []string{"operator", "oncall", "readonly"} {
		out, err := e.Bridge.CreateAgent(ctx, console, bridge.AgentInput{Name: "own-" + preset, Preset: preset})
		if err != nil || out.Agent.Owner != "alice" || out.Agent.Preset != preset {
			t.Fatalf("console owner preset %s: %+v %v", preset, out.Agent, err)
		}
	}
	if _, err := e.Bridge.UpdateAgent(ctx, console, "own-readonly", bridge.AgentUpdate{Preset: &op, Machines: &all}); err != nil {
		t.Fatalf("console owner widens within the role: %v", err)
	}
	if a, _ := e.Store.Agent("own-readonly"); a.Preset != "operator" || a.Access != store.AccessAll || len(a.Machines) != 0 || len(a.Scopes) != 0 {
		t.Fatalf("console owner result: %+v", a)
	}
	// someone below owner is still refused a preset above their role, as bad input, and may set their own
	bobP, _ := e.Store.AccountPrincipal("bob")
	bob := bridge.ActorOf(bobP, "console")
	audits, chats = len(e.Store.AuditLog(1000)), len(e.Bot.Texts)
	if _, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "too-big", Preset: "operator"}); !errors.Is(err, bridge.ErrBadInput) || !strings.Contains(err.Error(), "more than bob's role") {
		t.Fatalf("above the caller's role: %v", err)
	}
	if _, ok := e.Store.Agent("too-big"); ok {
		t.Fatal("the refused agent was created")
	}
	quiet(t, e, audits, chats)
	if out, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "bob-bot", Preset: "oncall"}); err != nil || out.Agent.Preset != "oncall" || out.Agent.Owner != "bob" {
		t.Fatalf("a preset within the role: %+v %v", out.Agent, err)
	}
}
