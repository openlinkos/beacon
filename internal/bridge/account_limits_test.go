package bridge_test

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// accounts sets up alice, an owner, and bob, on call, and returns the command line's actor.
func accounts(t *testing.T, e *testutil.Env) bridge.Actor {
	t.Helper()
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
	return cli
}

func linkCode(t *testing.T, l bridge.LinkView) string {
	t.Helper()
	u, err := url.Parse(l.URL)
	if err != nil || u.Query().Get("code") == "" {
		t.Fatalf("no code in %q", l.URL)
	}
	return u.Query().Get("code")
}

// TestAccountAdministrationStaysWithinTheCallersScopes: a credential with accounts:write invites, resets
// and changes only people whose role's scopes it holds, and gives only such roles. The command line and
// owners are not limited.
func TestAccountAdministrationStaysWithinTheCallersScopes(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := accounts(t, e)
	token := func(name string, scopes ...string) bridge.Actor {
		p, ok := e.Store.Authenticate(e.Token(t, name, scopes...))
		if !ok {
			t.Fatal("no token")
		}
		return bridge.ActorOf(p, "api")
	}
	bare := token("accounts-only", "accounts:write")
	oncall := token("oncall-admin", append(store.RoleScopes("oncall"), "accounts:write")...)
	audits, texts := len(e.Store.AuditLog(100)), len(e.Bot.Texts)

	viewer, operator, owner := "viewer", "operator", "owner"
	for what, err := range map[string]error{
		"invite a viewer with accounts:write alone": func() error { _, err := e.Bridge.Invite(ctx, bare, "viewer", ""); return err }(),
		"reset an owner with accounts:write alone":  func() error { _, err := e.Bridge.ResetLink(ctx, bare, "alice"); return err }(),
		"rename an owner with accounts:write alone": func() error {
			_, err := e.Bridge.UpdateUser(ctx, bare, "alice", bridge.UserChange{DisplayName: &owner})
			return err
		}(),
		"invite an owner from an on-call token": func() error { _, err := e.Bridge.Invite(ctx, oncall, "owner", ""); return err }(),
		"reset an owner from an on-call token":  func() error { _, err := e.Bridge.ResetLink(ctx, oncall, "alice"); return err }(),
		"disable an owner from an on-call token": func() error {
			d := true
			_, err := e.Bridge.UpdateUser(ctx, oncall, "alice", bridge.UserChange{Disabled: &d})
			return err
		}(),
		"promote someone beyond an on-call token": func() error {
			_, err := e.Bridge.UpdateUser(ctx, oncall, "bob", bridge.UserChange{Role: &operator})
			return err
		}(),
		"demote an owner from an on-call token": func() error {
			_, err := e.Bridge.UpdateUser(ctx, oncall, "alice", bridge.UserChange{Role: &viewer})
			return err
		}(),
	} {
		if !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "which you do not hold") {
			t.Errorf("%s: %v", what, err)
		}
	}
	if len(e.Store.AuditLog(100)) != audits || len(e.Bot.Texts) != texts || len(e.Store.Invitations()) != 0 || len(e.Store.ResetLinks()) != 0 {
		t.Error("a refused change was made, audited or echoed")
	}

	// within its scopes, the on-call token may act
	if _, err := e.Bridge.Invite(ctx, oncall, "oncall", ""); err != nil {
		t.Errorf("an on-call invitation from an on-call token: %v", err)
	}
	if _, err := e.Bridge.ResetLink(ctx, oncall, "bob"); err != nil {
		t.Errorf("an on-call person's reset from an on-call token: %v", err)
	}
	if _, err := e.Bridge.UpdateUser(ctx, oncall, "bob", bridge.UserChange{Role: &viewer}); err != nil {
		t.Errorf("an on-call person demoted by an on-call token: %v", err)
	}
	// owners and the command line are not limited
	alice, _ := e.Store.AccountPrincipal("alice")
	for _, who := range []bridge.Actor{cli, bridge.ActorOf(alice, "console")} {
		if _, err := e.Bridge.Invite(ctx, who, "owner", ""); err != nil {
			t.Errorf("%s inviting an owner: %v", who.Name, err)
		}
		if _, err := e.Bridge.ResetLink(ctx, who, "alice"); err != nil {
			t.Errorf("%s resetting an owner: %v", who.Name, err)
		}
	}
}

// TestLinksFollowTheirMaker: a link records who made it, and stops working when that principal could no
// longer make it, from the console, the API or the command line. A reset can also revoke the agents'
// tokens, which it otherwise leaves working.
func TestLinksFollowTheirMaker(t *testing.T) {
	rechecks := 0
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Recheck = func() int { rechecks++; return 0 } })
	ctx := context.Background()
	cli := accounts(t, e)
	p, _ := e.Store.Authenticate(e.Token(t, "people-script", store.PeopleScopes...))
	script := bridge.ActorOf(p, "api")

	inv, err := e.Bridge.Invite(ctx, script, "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	reset, err := e.Bridge.ResetLink(ctx, script, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Store.RevokeToken("people-script"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.Join(ctx, linkCode(t, inv), "mallory", "", "correct horse battery"); !errors.Is(err, store.ErrBadLink) {
		t.Errorf("an invitation of a revoked token: %v", err)
	}
	if _, err := e.Bridge.Reset(ctx, linkCode(t, reset), "a password for bob", false); !errors.Is(err, store.ErrBadLink) {
		t.Errorf("a reset link of a revoked token: %v", err)
	}

	// a reset ends the OAuth sign-ins of the person's agents; with revokeTokens their tokens too
	out, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "codex", Owner: "bob", Preset: "oncall", TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.NewAgentToken(ctx, cli, "codex", 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := e.Bridge.PersonTokens("bob"); len(got) != 2 {
		t.Fatalf("bob's agents' tokens: %v", got)
	}
	in, err := e.Store.CreateGrant(store.Grant{Agent: "codex", Client: "lbc_x", ClientName: "Client", Preset: "oncall", Expires: time.Now().Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}
	reset, _ = e.Bridge.ResetLink(ctx, cli, "bob")
	before := rechecks
	if _, err := e.Bridge.Reset(ctx, linkCode(t, reset), "a password for bob", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Store.Authenticate(in.Access); ok {
		t.Error("an OAuth sign-in of bob's agent outlives the reset")
	}
	if _, ok := e.Store.Authenticate(out.Secret); !ok {
		t.Error("the agent's token stopped without being asked to")
	}
	if rechecks == before {
		t.Error("the reset did not recheck the SSH channel")
	}
	reset, _ = e.Bridge.ResetLink(ctx, cli, "bob")
	if _, err := e.Bridge.Reset(ctx, linkCode(t, reset), "another password for bob", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Store.Authenticate(out.Secret); ok {
		t.Error("the agent's token outlives a reset that revokes it")
	}
	if got := e.Bridge.PersonTokens("bob"); len(got) != 0 {
		t.Errorf("tokens left: %v", got)
	}
	if !slices.ContainsFunc(e.Store.AuditLog(20), func(a store.AuditEntry) bool {
		return a.Action == "revoke_token" && a.Target == "codex-2" && a.Actor == "bob"
	}) {
		t.Errorf("audit: %+v", e.Store.AuditLog(20))
	}
	if !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "bob revoked the tokens of their agents with the reset: codex, codex-2") {
		t.Errorf("chat: %v", e.Bot.Texts)
	}
	if a, ok := e.Store.Agent("codex"); !ok || a.Owner != "bob" {
		t.Error("the agent went with its tokens")
	}

	// pending reset links are listed for admins and withdrawn
	reset, _ = e.Bridge.ResetLink(ctx, cli, "bob")
	bob := bridge.Actor{Name: "bob", Via: "console", Key: "user:bob"}
	if _, err := e.Bridge.ResetLinks(bob); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("someone without accounts:write lists reset links: %v", err)
	}
	if _, err := e.Bridge.WithdrawLink(ctx, bob, reset.ID); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("someone without accounts:write withdraws a link: %v", err)
	}
	links, err := e.Bridge.ResetLinks(cli)
	if err != nil || len(links) != 1 || links[0].User != "bob" || links[0].URL != "" {
		t.Fatalf("reset links: %+v %v", links, err)
	}
	if _, err := e.Bridge.WithdrawLink(ctx, cli, links[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.Reset(ctx, linkCode(t, reset), "yet another password", false); !errors.Is(err, store.ErrBadLink) {
		t.Errorf("a withdrawn reset link: %v", err)
	}
	if !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "cli:root withdrew a password reset link for bob via the command line") {
		t.Errorf("chat: %v", e.Bot.Texts)
	}
}
