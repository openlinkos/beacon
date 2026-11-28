package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

var cli = LinkMaker{Name: "cli:root", Key: LinkCLI}

func maker(name string) LinkMaker { return LinkMaker{Name: name, Key: "user:" + name} }

// TestLinksWorkOnlyWhileTheirMakerMay: an invitation or a reset link works only while whoever made it
// could make it now, read afresh when it is opened and when it is used. Disabling or demoting the maker,
// or revoking the token that made it, deletes the links it made.
func TestLinksWorkOnlyWhileTheirMakerMay(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	join := func(role, name string, by LinkMaker) {
		t.Helper()
		code, _, err := s.CreateInvitation(role, "", by)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Join(code, name, "", "correct horse battery"); err != nil {
			t.Fatalf("join %s: %v", name, err)
		}
	}
	join("owner", "mallory", cli)
	join("oncall", "bob", maker("alice"))

	// an owner's links, then the owner is disabled: they stop working and are gone
	inv, _, _ := s.CreateInvitation("owner", "", maker("mallory"))
	reset, _, _ := s.CreateResetLink("alice", maker("mallory"))
	if _, err := s.Link(LinkInvite, inv); err != nil {
		t.Fatalf("the invitation before: %v", err)
	}
	if _, err := s.UpdateAccount("mallory", func(a *Account) error { a.Disabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(inv, "eve", "", "correct horse battery"); !errors.Is(err, ErrBadLink) {
		t.Errorf("an invitation of a disabled owner: %v", err)
	}
	if _, err := s.Reset(reset, "a password of eve's"); !errors.Is(err, ErrBadLink) {
		t.Errorf("a reset link of a disabled owner: %v", err)
	}
	if len(s.Invitations()) != 0 || len(s.ResetLinks()) != 0 {
		t.Errorf("a disabled owner's links are listed: %v %v", s.Invitations(), s.ResetLinks())
	}
	// enabling them again brings nothing back
	if _, err := s.UpdateAccount("mallory", func(a *Account) error { a.Disabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(inv, "eve", "", "correct horse battery"); !errors.Is(err, ErrBadLink) {
		t.Errorf("an invitation of an owner disabled and enabled again: %v", err)
	}

	// demoted: the same
	inv, _, _ = s.CreateInvitation("viewer", "", maker("mallory"))
	if _, err := s.UpdateAccount("mallory", func(a *Account) error { a.Role = "operator"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Link(LinkInvite, inv); !errors.Is(err, ErrBadLink) {
		t.Errorf("an invitation of a demoted owner: %v", err)
	}

	// a token of its own: read afresh, so a narrowed or expired token's links stop, and a revoked
	// token's go
	_, _, _ = s.CreateToken("admin", PeopleScopes, 2*time.Hour, "cli:root")
	_, _ = s.UpdateToken("admin", func(t *Token) error { t.MachineAccess = AllAccess(); return nil }) // an on-call person reaches all machines
	byToken := LinkMaker{Name: "admin", Key: "token:admin"}
	inv, _, _ = s.CreateInvitation("oncall", "", byToken)
	if _, err := s.Link(LinkInvite, inv); err != nil {
		t.Fatalf("a token's invitation: %v", err)
	}
	if _, err := s.UpdateToken("admin", func(t *Token) error { t.Scopes = []string{"status:read", "sessions:read"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(inv, "eve", "", "correct horse battery"); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("an invitation of a token without accounts:write: %v", err)
	}
	_, _ = s.UpdateToken("admin", func(t *Token) error { t.Scopes = PeopleScopes; return nil })
	if _, err := s.Link(LinkInvite, inv); err != nil {
		t.Errorf("the invitation once the token has its scopes again: %v", err)
	}
	*now = now.Add(3 * time.Hour)
	if _, err := s.Link(LinkInvite, inv); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("an invitation of an expired token: %v", err)
	}
	*now = now.Add(-3 * time.Hour)
	if err := s.RevokeToken("admin"); err != nil {
		t.Fatal(err)
	}
	_, _, _ = s.CreateToken("admin", PeopleScopes, 2*time.Hour, "cli:root") // the same name again
	if _, err := s.Link(LinkInvite, inv); !errors.Is(err, ErrBadLink) || errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("an invitation of a revoked token, under a new token of its name: %v", err)
	}

	// a maker is held to the role it gives, or the role of the account it resets now
	_, _, _ = s.CreateToken("people", []string{"accounts:write", "status:read", "sessions:read", "alerts:write", "machines:ssh"}, 2*time.Hour, "cli:root")
	_, _ = s.UpdateToken("people", func(t *Token) error { t.MachineAccess = AllAccess(); return nil })
	byPeople := LinkMaker{Name: "people", Key: "token:people"}
	inv, _, _ = s.CreateInvitation("owner", "", byPeople)
	if _, err := s.Join(inv, "eve", "", "correct horse battery"); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("an owner's invitation from a token without an owner's scopes: %v", err)
	}
	reset, _, _ = s.CreateResetLink("bob", byPeople)
	if _, err := s.Link(LinkReset, reset); err != nil {
		t.Errorf("a reset link for an on-call person from a token with on-call scopes: %v", err)
	}
	if _, err := s.UpdateAccount("bob", func(a *Account) error { a.Role = "owner"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset(reset, "a password of eve's"); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("a reset link for someone promoted beyond its maker: %v", err)
	}

	// and to what the role reaches: an on-call person reaches every machine that is not critical, so a
	// maker narrowed to some machines, or a person with a critical machine named, withdraws the link
	if _, err := s.UpdateAccount("bob", func(a *Account) error { a.Role = "oncall"; return nil }); err != nil {
		t.Fatal(err)
	}
	inv, _, _ = s.CreateInvitation("oncall", "", byPeople)
	reset, _, _ = s.CreateResetLink("bob", byPeople)
	if _, err := s.Link(LinkReset, reset); err != nil {
		t.Errorf("a reset link for an on-call person from a token reaching all machines: %v", err)
	}
	_, _ = s.UpdateToken("people", func(t *Token) error { t.MachineAccess = NamedAccess("box"); return nil })
	if _, err := s.Link(LinkInvite, inv); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("an on-call invitation from a token narrowed to one machine: %v", err)
	}
	if _, err := s.Link(LinkReset, reset); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("an on-call reset link from a token narrowed to one machine: %v", err)
	}
	_, _ = s.UpdateToken("people", func(t *Token) error { t.MachineAccess = AllAccess(); return nil })
	if _, _, err := s.SetCriticalMachines("bob", NamedAccess("ca")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Link(LinkReset, reset); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("a token's reset link for someone with a critical machine named: %v", err)
	}
	if _, _, err := s.SetCriticalMachines("bob", NamedAccess()); err != nil {
		t.Fatal(err)
	}

	// a link that names no maker, made before makers were recorded: refused, and not listed
	code := "a-code-from-an-older-beacon"
	_ = s.db.Update(func(tx kv.Tx) error {
		v, _ := json.Marshal(AccountLink{Hash: hashLink(code), Kind: LinkInvite, Role: "viewer", CreatedBy: "alice", Created: *now, Expires: now.Add(time.Hour)})
		return tx.Put(bucketLinks, []byte(hashLink(code)), v)
	})
	if _, err := s.Link(LinkInvite, code); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("a link without a maker: %v", err)
	}
	if _, err := s.Join(code, "eve", "", "correct horse battery"); !errors.Is(err, ErrLinkWithdrawn) {
		t.Errorf("joining with a link without a maker: %v", err)
	}
	for _, l := range s.Invitations() {
		if l.Maker == "" {
			t.Errorf("a link without a maker is listed: %+v", l)
		}
	}

	// the command line's links always work
	inv, _, _ = s.CreateInvitation("owner", "", cli)
	if _, err := s.Join(inv, "carol", "", "correct horse battery"); err != nil {
		t.Errorf("the command line's invitation: %v", err)
	}
	if _, _, err := s.CreateInvitation("viewer", "", LinkMaker{Name: "someone"}); err == nil {
		t.Error("an invitation that names no maker was made")
	}
}

// TestNewPasswordEndsOAuthSignInsAndResetLinks: a new password, by a reset link or by the person, ends the
// OAuth sign-ins of the person's agents and the other reset links for the account. A reset also withdraws
// the links the person made. The agents' tokens keep working.
func TestNewPasswordEndsOAuthSignInsAndResetLinks(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "bob", "owner")
	invite(t, s, "carol", "oncall")
	if _, err := s.CreateAgent(Agent{Name: "codex", Owner: "bob", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAgent(Agent{Name: "carols", Owner: "carol", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	tokenSecret, _, err := s.CreateAgentToken("codex", 24*time.Hour, "bob")
	if err != nil {
		t.Fatal(err)
	}
	grant := func(agent string) Issued {
		t.Helper()
		in, err := s.CreateGrant(Grant{Agent: agent, Client: "lbc_x", ClientName: "Client", Scope: "oncall", Preset: "oncall", Expires: now.Add(30 * 24 * time.Hour)}, true)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	bobs, carols := grant("codex"), grant("carols")

	first, _, _ := s.CreateResetLink("bob", maker("alice"))
	second, _, _ := s.CreateResetLink("bob", maker("alice"))
	bobsInvitation, _, _ := s.CreateInvitation("viewer", "", maker("bob"))
	alicesInvitation, _, _ := s.CreateInvitation("viewer", "", maker("alice"))
	if _, err := s.Reset(first, "bob's brand new password"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate(bobs.Access); ok {
		t.Error("an OAuth access token of bob's agent outlives the reset")
	}
	if _, err := s.RefreshGrant(bobs.Refresh, "lbc_x"); err == nil {
		t.Error("an OAuth refresh token of bob's agent outlives the reset")
	}
	if len(s.Grants("codex")) != 0 {
		t.Errorf("bob's agent keeps its grants: %v", s.Grants("codex"))
	}
	if p, ok := s.Authenticate(tokenSecret); !ok || p.Agent != "codex" {
		t.Error("the agent's token stopped with the reset")
	}
	if _, ok := s.Authenticate(carols.Access); !ok {
		t.Error("another person's agent lost its sign-in")
	}
	if _, err := s.Reset(second, "another one for bob"); !errors.Is(err, ErrBadLink) {
		t.Errorf("another reset link for bob outlives the reset: %v", err)
	}
	if _, err := s.Link(LinkInvite, bobsInvitation); !errors.Is(err, ErrBadLink) {
		t.Errorf("an invitation bob made outlives the reset of his password: %v", err)
	}
	if _, err := s.Link(LinkInvite, alicesInvitation); err != nil {
		t.Errorf("someone else's invitation went with bob's reset: %v", err)
	}

	// the person changes it themself: their agents' sign-ins and the reset links for them end; the
	// invitations they made stay
	bobs = grant("codex")
	pending, _, _ := s.CreateResetLink("bob", maker("alice"))
	bobsInvitation, _, _ = s.CreateInvitation("viewer", "", maker("bob"))
	if err := s.ChangePassword("bob", "bob's brand new password", "bob's newer password", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate(bobs.Access); ok {
		t.Error("an OAuth sign-in of bob's agent outlives his new password")
	}
	if _, err := s.Link(LinkReset, pending); !errors.Is(err, ErrBadLink) {
		t.Errorf("a reset link outlives a new password: %v", err)
	}
	if _, err := s.Link(LinkInvite, bobsInvitation); err != nil {
		t.Errorf("an invitation bob made went with his own new password: %v", err)
	}
	if _, ok := s.Authenticate(tokenSecret); !ok {
		t.Error("the agent's token stopped with the new password")
	}
	if _, ok := s.Authenticate(carols.Access); !ok {
		t.Error("another person's agent lost its sign-in")
	}
}
