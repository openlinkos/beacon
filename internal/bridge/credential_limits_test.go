package bridge_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func sshKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// machines enrols box and a critical machine, ca.
func machines(t *testing.T, e *testutil.Env) {
	t.Helper()
	for _, m := range []store.Machine{{Name: "box", Address: "box.example.com:22", User: "beacon"}, {Name: "ca", Address: "ca.example.com:22", User: "beacon", Critical: true}} {
		if err := e.Store.PutMachine(m); err != nil {
			t.Fatal(err)
		}
	}
}

// tokenActor makes a token of its own with scopes and machine access, and returns its actor.
func tokenActor(t *testing.T, e *testutil.Env, name string, access store.MachineAccess, scopes ...string) bridge.Actor {
	t.Helper()
	secret, _, err := e.Store.CreateToken(name, scopes, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.UpdateToken(name, func(tok *store.Token) error { tok.MachineAccess = access; return nil }); err != nil {
		t.Fatal(err)
	}
	p, ok := e.Store.Authenticate(secret)
	if !ok {
		t.Fatal("no token")
	}
	return bridge.ActorOf(p, "api")
}

// TestAgentCredentialsStayWithinWhoGivesThem: a new token for an agent, or a key added to it, comes from
// its owner, an owner, the command line, or a caller holding every scope the agent asks for and machine
// access covering the agent's, its critical machines included: never more than the caller holds.
func TestAgentCredentialsStayWithinWhoGivesThem(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := accounts(t, e)
	machines(t, e)
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "deploy", Owner: "alice", Preset: "operator", Machines: store.AllAccess("ca")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "helper", Owner: "bob", Preset: "oncall", Machines: store.NamedAccess("box")}); err != nil {
		t.Fatal(err)
	}
	operator := append(store.RoleScopes("operator"), "accounts:write")
	bare := tokenActor(t, e, "accounts-only", store.NamedAccess(), "accounts:write")
	wide := tokenActor(t, e, "operator-admin", store.AllAccess(), operator...)
	named := tokenActor(t, e, "named-admin", store.AllAccess("ca"), operator...)
	audits, texts := len(e.Store.AuditLog(100)), len(e.Bot.Texts)

	for what, err := range map[string]error{
		"a token for an owner's agent from accounts:write alone": func() error {
			_, err := e.Bridge.NewAgentToken(ctx, bare, "deploy", 365*24*time.Hour)
			return err
		}(),
		"a key for an owner's agent from accounts:write alone": func() error {
			_, err := e.Bridge.AddSSHKey(ctx, bare, "deploy", sshKeyLine(t), "")
			return err
		}(),
		"a token for an agent with a critical machine the caller does not name": func() error {
			_, err := e.Bridge.NewAgentToken(ctx, wide, "deploy", time.Hour)
			return err
		}(),
		"a key for an agent with a critical machine the caller does not name": func() error {
			_, err := e.Bridge.AddSSHKey(ctx, wide, "deploy", sshKeyLine(t), "")
			return err
		}(),
		"a token for an agent that asks for scopes the caller lacks": func() error {
			_, err := e.Bridge.NewAgentToken(ctx, bare, "helper", time.Hour)
			return err
		}(),
	} {
		if !errors.Is(err, bridge.ErrForbidden) {
			t.Errorf("%s: %v", what, err)
		}
	}
	if n := len(e.Store.AgentTokens("deploy")) + len(e.Store.AgentTokens("helper")); n != 0 {
		t.Errorf("%d tokens were issued", n)
	}
	if a, _ := e.Store.Agent("deploy"); len(a.SSHKeys) != 0 {
		t.Errorf("keys were added: %+v", a.SSHKeys)
	}
	if len(e.Store.AuditLog(100)) != audits || len(e.Bot.Texts) != texts {
		t.Error("a refused credential was audited or echoed")
	}

	// within the caller, it may; and the agent's owner, an owner and the command line may
	if _, err := e.Bridge.NewAgentToken(ctx, named, "deploy", time.Hour); err != nil {
		t.Errorf("a caller holding all the agent may use: %v", err)
	}
	if _, err := e.Bridge.AddSSHKey(ctx, named, "deploy", sshKeyLine(t), ""); err != nil {
		t.Errorf("a key from a caller holding all the agent may use: %v", err)
	}
	if _, err := e.Bridge.NewAgentToken(ctx, wide, "helper", time.Hour); err != nil {
		t.Errorf("a token for an agent within the caller: %v", err)
	}
	alice, _ := e.Store.AccountPrincipal("alice")
	bob, _ := e.Store.AccountPrincipal("bob")
	for _, c := range []struct {
		who   bridge.Actor
		agent string
	}{{bridge.ActorOf(bob, "console"), "helper"}, {bridge.ActorOf(alice, "console"), "helper"}, {bridge.ActorOf(alice, "console"), "deploy"}, {cli, "deploy"}} {
		if _, err := e.Bridge.NewAgentToken(ctx, c.who, c.agent, time.Hour); err != nil {
			t.Errorf("%s giving %s a token: %v", c.who.Name, c.agent, err)
		}
	}
}

// TestAccountRolesStayWithinWhatTheCallerReaches: giving a role, or acting on a person with it, needs
// machine access that covers what the role reaches. Only an owner gives or acts on the owner role, and
// on a person with critical machines named; a role that reaches machines needs access to all machines.
// Links made within that stop working once the maker no longer reaches what the role does.
func TestAccountRolesStayWithinWhatTheCallerReaches(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := accounts(t, e)
	machines(t, e)
	inv, _ := e.Bridge.Invite(ctx, cli, "viewer", "")
	if _, err := e.Bridge.Join(ctx, linkCode(t, inv), "carol", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ = e.Bridge.Invite(ctx, cli, "oncall", "")
	if _, err := e.Bridge.Join(ctx, linkCode(t, inv), "dave", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.SetCriticalMachines(ctx, cli, "dave", store.NamedAccess("ca")); err != nil {
		t.Fatal(err)
	}
	every := store.PeopleScopes
	oneBox := tokenActor(t, e, "one-box", store.NamedAccess("box"), every...)
	all := tokenActor(t, e, "all-machines", store.AllAccess(), every...)
	allCA := tokenActor(t, e, "all-and-ca", store.AllAccess("ca"), every...)
	audits, texts := len(e.Store.AuditLog(100)), len(e.Bot.Texts)

	owner, operator, viewer := "owner", "operator", "viewer"
	enable, disable := false, true
	for what, err := range map[string]error{
		"invite an owner from a token reaching one machine": func() error { _, err := e.Bridge.Invite(ctx, oneBox, "owner", ""); return err }(),
		"invite an owner from a token reaching all machines and the critical one": func() error {
			_, err := e.Bridge.Invite(ctx, allCA, "owner", "")
			return err
		}(),
		"reset an owner's password from a token": func() error { _, err := e.Bridge.ResetLink(ctx, all, "alice"); return err }(),
		"promote someone to owner from a token": func() error {
			_, err := e.Bridge.UpdateUser(ctx, allCA, "bob", bridge.UserChange{Role: &owner})
			return err
		}(),
		"disable an owner from a token": func() error {
			_, err := e.Bridge.UpdateUser(ctx, all, "alice", bridge.UserChange{Disabled: &disable})
			return err
		}(),
		"invite an on-call person from one machine": func() error { _, err := e.Bridge.Invite(ctx, oneBox, "oncall", ""); return err }(),
		"reset an on-call person from one machine":  func() error { _, err := e.Bridge.ResetLink(ctx, oneBox, "bob"); return err }(),
		"promote a viewer to operator from one machine": func() error {
			_, err := e.Bridge.UpdateUser(ctx, oneBox, "carol", bridge.UserChange{Role: &operator})
			return err
		}(),
		"demote an on-call person from one machine": func() error {
			_, err := e.Bridge.UpdateUser(ctx, oneBox, "bob", bridge.UserChange{Role: &viewer})
			return err
		}(),
		"reset someone with a critical machine named from a token": func() error { _, err := e.Bridge.ResetLink(ctx, allCA, "dave"); return err }(),
		"promote someone with a critical machine named from a token": func() error {
			_, err := e.Bridge.UpdateUser(ctx, allCA, "dave", bridge.UserChange{Role: &operator})
			return err
		}(),
		"enable someone with a critical machine named from a token": func() error {
			_, err := e.Bridge.UpdateUser(ctx, allCA, "dave", bridge.UserChange{Disabled: &enable})
			return err
		}(),
	} {
		if !errors.Is(err, bridge.ErrForbidden) {
			t.Errorf("%s: %v", what, err)
		}
	}
	if a, _ := e.Store.Account("carol"); a.Role != "viewer" {
		t.Errorf("carol is %s", a.Role)
	}
	if a, _ := e.Store.Account("alice"); a.Disabled || a.Role != "owner" {
		t.Errorf("alice: %+v", a)
	}
	if len(e.Store.AuditLog(100)) != audits || len(e.Bot.Texts) != texts || len(e.Store.Invitations()) != 0 || len(e.Store.ResetLinks()) != 0 {
		t.Error("a refused change was made, audited or echoed")
	}

	// a viewer's role reaches no machine, so one machine is enough; all machines for an on-call person
	if _, err := e.Bridge.Invite(ctx, oneBox, "viewer", ""); err != nil {
		t.Errorf("a viewer's invitation from one machine: %v", err)
	}
	if _, err := e.Bridge.UpdateUser(ctx, all, "carol", bridge.UserChange{Role: &operator}); err != nil {
		t.Errorf("a promotion to operator from all machines: %v", err)
	}
	reset, err := e.Bridge.ResetLink(ctx, all, "bob")
	if err != nil {
		t.Fatalf("an on-call person's reset from all machines: %v", err)
	}
	inv, err = e.Bridge.Invite(ctx, all, "oncall", "")
	if err != nil {
		t.Fatalf("an on-call invitation from all machines: %v", err)
	}
	// an owner and the command line act on owners and on critical machines named
	ap, _ := e.Store.AccountPrincipal("alice")
	for _, who := range []bridge.Actor{cli, bridge.ActorOf(ap, "console")} {
		if _, err := e.Bridge.ResetLink(ctx, who, "dave"); err != nil {
			t.Errorf("%s resetting someone with a critical machine: %v", who.Name, err)
		}
		if _, err := e.Bridge.Invite(ctx, who, "owner", ""); err != nil {
			t.Errorf("%s inviting an owner: %v", who.Name, err)
		}
	}

	// the links stop working once their maker reaches only some machines
	if _, err := e.Store.UpdateToken("all-machines", func(tok *store.Token) error { tok.MachineAccess = store.NamedAccess("box"); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.Reset(ctx, linkCode(t, reset), "a password for bob", false); !errors.Is(err, store.ErrLinkWithdrawn) {
		t.Errorf("a reset link of a maker narrowed to one machine: %v", err)
	}
	if _, err := e.Bridge.Join(ctx, linkCode(t, inv), "erin", "", "correct horse battery"); !errors.Is(err, store.ErrLinkWithdrawn) {
		t.Errorf("an invitation of a maker narrowed to one machine: %v", err)
	}
}

// TestAccessIsRecordedOrRefused: while audit entries cannot be written, invitations, reset links,
// promotions, enabling, widening an agent, naming critical machines and allowing an OAuth client are not
// made; narrowing and taking access away still are.
func TestAccessIsRecordedOrRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Store = st })
	e.Store = st
	ctx := context.Background()
	cli := accounts(t, e)
	machines(t, e)
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "codex", Owner: "bob", Preset: "readonly", Machines: store.NamedAccess("box")}); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if _, err := e.Bridge.UpdateUser(ctx, cli, "bob", bridge.UserChange{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}
	alice, _ := e.Store.AccountPrincipal("alice")
	owner := bridge.ActorOf(alice, "console")
	operator, oncall := "operator", "oncall"
	enable := false
	wider := store.AllAccess()
	for what, err := range map[string]error{
		"an invitation": func() error { _, err := e.Bridge.Invite(ctx, cli, "operator", ""); return err }(),
		"a reset link":  func() error { _, err := e.Bridge.ResetLink(ctx, cli, "alice"); return err }(),
		"enabling bob": func() error {
			_, err := e.Bridge.UpdateUser(ctx, cli, "bob", bridge.UserChange{Disabled: &enable})
			return err
		}(),
		"promoting bob": func() error {
			_, err := e.Bridge.UpdateUser(ctx, cli, "bob", bridge.UserChange{Role: &operator})
			return err
		}(),
		"widening an agent": func() error {
			_, err := e.Bridge.UpdateAgent(ctx, cli, "codex", bridge.AgentUpdate{Preset: &oncall, Machines: &wider})
			return err
		}(),
		"naming a critical machine": func() error {
			_, err := e.Bridge.SetCriticalMachines(ctx, cli, "bob", store.NamedAccess("ca"))
			return err
		}(),
		"allowing an OAuth client": func() error {
			_, _, err := e.Bridge.OAuthAgent(ctx, owner, "lbc_fixture", "Fixture", "fixture", "oncall", store.NamedAccess("box"), time.Now().Add(24*time.Hour))
			return err
		}(),
	} {
		if !errors.Is(err, bridge.ErrUnrecorded) {
			t.Errorf("%s without its record: %v", what, err)
		}
	}
	if len(e.Store.Invitations()) != 0 || len(e.Store.ResetLinks()) != 0 {
		t.Error("a link was made without its record")
	}
	if a, _ := e.Store.Account("bob"); !a.Disabled || a.Role != "oncall" || len(a.CriticalMachines) != 0 {
		t.Errorf("bob changed without a record: %+v", a)
	}
	if a, _ := e.Store.Agent("codex"); a.Preset != "readonly" || a.IsAll() {
		t.Errorf("the agent widened without a record: %+v", a)
	}
	if _, ok := e.Store.Agent("fixture"); ok {
		t.Error("an OAuth client's agent was made without its record")
	}

	// narrowing and taking away go ahead
	none := store.NamedAccess()
	if _, err := e.Bridge.UpdateAgent(ctx, cli, "codex", bridge.AgentUpdate{Machines: &none}); err != nil {
		t.Errorf("narrowing an agent: %v", err)
	}
	if a, _ := e.Store.Agent("codex"); !a.None() {
		t.Errorf("the agent was not narrowed: %+v", a.MachineAccess)
	}
	if _, err := e.Bridge.UpdateUser(ctx, cli, "alice", bridge.UserChange{DisplayName: &operator}); err != nil {
		t.Errorf("renaming: %v", err)
	}
}

// TestResetRemovesThePersonsKeys: a password reset removes the person's own SSH keys and closes what they
// had open, audited and echoed; changing one's own password keeps them.
func TestResetRemovesThePersonsKeys(t *testing.T) {
	rechecks := 0
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Recheck = func() int { rechecks++; return 0 } })
	ctx := context.Background()
	cli := accounts(t, e)
	bob, _ := e.Store.AccountPrincipal("bob")
	k, err := e.Bridge.AddSSHKey(ctx, bridge.ActorOf(bob, "console"), "", sshKeyLine(t), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Bridge.ChangePassword(ctx, bridge.ActorOf(bob, "console"), "correct horse battery", "a new password for bob", ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.Store.Account("bob"); len(a.SSHKeys) != 1 {
		t.Errorf("changing one's own password removed the keys: %+v", a.SSHKeys)
	}
	reset, err := e.Bridge.ResetLink(ctx, cli, "bob")
	if err != nil {
		t.Fatal(err)
	}
	before := rechecks
	if _, err := e.Bridge.Reset(ctx, linkCode(t, reset), "another password for bob", false); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.Store.Account("bob"); len(a.SSHKeys) != 0 {
		t.Errorf("keys outlive the reset: %+v", a.SSHKeys)
	}
	if holder := e.Store.KeyHolder(k.Fingerprint); holder != "" {
		t.Errorf("the key is still held by %s", holder)
	}
	if p, _ := e.Store.AccountPrincipal("bob"); e.Store.CredentialLive("key:"+k.Fingerprint, p) {
		t.Error("the key still signs bob in")
	}
	if rechecks == before {
		t.Error("the reset did not close what the keys had open")
	}
	if !slices.ContainsFunc(e.Store.AuditLog(20), func(a store.AuditEntry) bool {
		return a.Action == "ssh_key_remove" && a.Target == "bob" && strings.HasPrefix(a.Detail, k.Fingerprint)
	}) {
		t.Errorf("audit: %+v", e.Store.AuditLog(20))
	}
	if !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "The reset removed the SSH keys of bob: "+k.Fingerprint) {
		t.Errorf("chat: %v", e.Bot.Texts)
	}
}

// TestAgentTokensAreDeletedByTheirOwnPeople: an agent's token is deleted by the agent, its owner or an
// owner, as on the agent's page, not by any person with machines:write; a refusal reads like a token that
// does not exist.
func TestAgentTokensAreDeletedByTheirOwnPeople(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := accounts(t, e)
	inv, _ := e.Bridge.Invite(ctx, cli, "operator", "")
	if _, err := e.Bridge.Join(ctx, linkCode(t, inv), "carol", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "deploy", Owner: "alice", Preset: "operator", Machines: store.AllAccess(), TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "carols", Owner: "carol", Preset: "operator", Machines: store.AllAccess(), TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	carol, _ := e.Store.AccountPrincipal("carol")
	who := bridge.ActorOf(carol, "console")
	err := bridge.TokenRefusal(e.Bridge.RevokeToken(ctx, who, "deploy"), "deploy")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an operator deletes an owner's agent's token: %v", err)
	}
	if len(e.Store.AgentTokens("deploy")) != 1 {
		t.Error("the owner's agent's token went")
	}
	if err := e.Bridge.RevokeToken(ctx, who, "carols"); err != nil {
		t.Errorf("carol deletes her own agent's token: %v", err)
	}
	alice, _ := e.Store.AccountPrincipal("alice")
	if err := e.Bridge.RevokeToken(ctx, bridge.ActorOf(alice, "console"), "deploy"); err != nil {
		t.Errorf("the owner deletes her agent's token: %v", err)
	}
}
