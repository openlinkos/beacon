package bridge_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestIdentityAdministrationRequiresOwnAuthority(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	code, _, err := e.Store.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	account, _ := e.Store.AccountPrincipal("alice")
	token, _ := e.Store.Authenticate(e.Token(t, "owner-script", store.PeopleScopes...))
	for _, admin := range []bridge.Actor{cli, bridge.ActorOf(account, "console")} {
		if _, err := e.Bridge.Invite(ctx, admin, "owner", "test invitation"); err != nil {
			t.Fatalf("%s could not invite an owner: %v", admin.Via, err)
		}
		if _, err := e.Bridge.ResetLink(ctx, admin, "alice"); err != nil {
			t.Fatalf("%s could not request an account reset: %v", admin.Via, err)
		}
	}
	// a standalone token reaches the roles below an owner's, never an owner
	if _, err := e.Bridge.Invite(ctx, bridge.ActorOf(token, "api"), "operator", "test invitation"); err != nil {
		t.Fatalf("a token could not invite an operator: %v", err)
	}
	if _, err := e.Bridge.ResetLink(ctx, bridge.ActorOf(token, "api"), "alice"); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("a token made an owner's reset link: %v", err)
	}
	out, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{
		Name: "existing-delegate", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"accounts:write", "machines:ssh"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := e.Store.Authenticate(out.Secret)
	if !ok || !p.Has("accounts:write") {
		t.Fatal("the regression fixture must exercise previously issued agent scopes")
	}
	agent := bridge.ActorOf(p, "mcp")
	if _, err := e.Bridge.SSHKeys(agent, ""); err != nil {
		t.Fatalf("an agent cannot see its own SSH keys: %v", err)
	}
	audits, messages := len(e.Store.AuditLog(100)), len(e.Bot.Texts)
	if _, err := e.Bridge.Invite(ctx, agent, "owner", "test invitation"); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an agent could invite an owner: %v", err)
	}
	name := "changed"
	if _, err := e.Bridge.UpdateUser(ctx, agent, "alice", bridge.UserChange{DisplayName: &name}); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an agent could update a person's account: %v", err)
	}
	if _, err := e.Bridge.ResetLink(ctx, agent, "alice"); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an agent could request an account reset: %v", err)
	}
	if _, err := e.Bridge.NewAgentToken(ctx, agent, "existing-delegate", time.Hour); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an agent could issue a new static credential: %v", err)
	}
	if len(e.Store.AuditLog(100)) != audits || len(e.Bot.Texts) != messages {
		t.Error("refused administration changed the audit or chat")
	}
}
