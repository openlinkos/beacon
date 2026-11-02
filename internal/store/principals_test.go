package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAgentsAndPrincipals(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "bob", "operator")

	if _, err := s.CreateAgent(Agent{Name: "claude-code", Owner: "nobody", Preset: "oncall"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an agent of nobody: %v", err)
	}
	if _, err := s.CreateAgent(Agent{Name: "x", Owner: "bob", Preset: "oncall"}); err == nil {
		t.Error("a one-letter name")
	}
	if _, err := s.CreateAgent(Agent{Name: "bot", Owner: "bob", Preset: "admin"}); err == nil {
		t.Error("an unknown preset")
	}
	if _, err := s.CreateAgent(Agent{Name: "bot", Owner: "bob", Preset: PresetCustom, Scopes: []string{"probe:write"}}); err == nil {
		t.Error("a custom agent with a machine's scope")
	}
	a, err := s.CreateAgent(Agent{Name: "claude-code", Owner: "bob", Preset: "operator", Machines: []string{"edge-2", "edge-1", "edge-1"}, CreatedBy: "bob"})
	if err != nil || !slices.Equal(a.Machines, []string{"edge-1", "edge-2"}) {
		t.Fatalf("create: %+v %v", a, err)
	}
	if _, err := s.CreateAgent(Agent{Name: "claude-code", Owner: "alice", Preset: "oncall"}); !errors.Is(err, ErrExists) {
		t.Errorf("a taken name: %v", err)
	}

	// an agent without a credential does not sign in by key
	if _, ok := s.AgentPrincipal("claude-code"); ok {
		t.Error("an agent without credentials resolves")
	}
	secret, tok, err := s.CreateAgentToken("claude-code", 365*24*time.Hour, "bob")
	if err != nil || tok.Name != "claude-code" || tok.Agent != "claude-code" || len(tok.Scopes) != 0 {
		t.Fatalf("token: %+v %v", tok, err)
	}
	_, tok2, _ := s.CreateAgentToken("claude-code", 30*24*time.Hour, "bob")
	if tok2.Name != "claude-code-2" {
		t.Errorf("the second token: %s", tok2.Name)
	}
	p, ok := s.Authenticate(secret)
	if !ok || p.Name() != "bob / claude-code" || p.Key() != "agent:claude-code" || p.Token != "claude-code" || !p.Has("machines:write") || p.Has("config:write") ||
		!p.Reaches("edge-1") || p.Reaches("edge-3") {
		t.Fatalf("the agent's token: %+v %v", p, ok)
	}
	// the machine tokens' gateway never takes an agent's token for a machine scope
	if raw, _ := s.VerifyToken(secret); raw.Has("probe:write") || len(raw.Scopes) != 0 {
		t.Errorf("an agent's token carries scopes of its own: %v", raw.Scopes)
	}

	// demoting the owner limits the agent at once; disabling the owner stops it
	if _, err := s.UpdateAccount("bob", func(x *Account) error { x.Role = "viewer"; return nil }); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Authenticate(secret); p.Has("machines:ssh") || !p.Has("status:read") || !slices.Equal(p.Scopes, []string{"status:read", "sessions:read"}) {
		t.Errorf("after demoting the owner: %v", p.Scopes)
	}
	if p, ok := s.Principal("agent:claude-code"); !ok || p.Has("machines:ssh") {
		t.Errorf("by key after demoting: %+v %v", p, ok)
	}
	_, _ = s.UpdateAccount("bob", func(x *Account) error { x.Role = "owner"; return nil })
	if p, _ := s.Authenticate(secret); p.Has("config:write") || !p.Has("machines:write") {
		t.Errorf("the preset still caps it: %v", p.Scopes)
	}
	_, _ = s.UpdateAccount("bob", func(x *Account) error { x.Disabled = true; return nil })
	if _, ok := s.Authenticate(secret); ok {
		t.Error("an agent of a disabled owner signs in")
	}
	if _, ok := s.AccountPrincipal("bob"); ok {
		t.Error("a disabled person resolves")
	}
	_, _ = s.UpdateAccount("bob", func(x *Account) error { x.Disabled = false; x.Role = "operator"; return nil })

	// SSH keys: one principal each
	k := SSHKey{Key: "ssh-ed25519 AAAA1", Fingerprint: "SHA256:one"}
	if _, err := s.AddAgentSSHKey("claude-code", k); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAgent(Agent{Name: "codex", Owner: "alice", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAgentSSHKey("codex", k); !errors.Is(err, ErrExists) {
		t.Errorf("a key held by another agent: %v", err)
	}
	_, legacy, _ := s.CreateToken("legacy", []string{"status:read", "machines:ssh"}, 24*time.Hour, "test")
	_, _ = s.UpdateToken(legacy.Name, func(t *Token) error {
		t.SSHKeys = []SSHKey{{Key: "ssh-ed25519 AAAA2", Fingerprint: "SHA256:two"}}
		return nil
	})
	if _, err := s.AddAgentSSHKey("codex", SSHKey{Fingerprint: "SHA256:two"}); !errors.Is(err, ErrExists) {
		t.Errorf("a key held by a token: %v", err)
	}
	if p, ok := s.PrincipalBySSHKey("SHA256:one"); !ok || p.Name() != "bob / claude-code" || !p.Expires.Equal(now.Add(365*24*time.Hour)) {
		t.Errorf("by key: %+v %v", p, ok)
	}
	if p, ok := s.PrincipalBySSHKey("SHA256:two"); !ok || p.Name() != "legacy" || !p.Has("machines:ssh") {
		t.Errorf("a legacy token by key: %+v %v", p, ok)
	}
	if _, err := s.RemoveAgentSSHKey("claude-code", "SHA256:nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a key it lacks: %v", err)
	}

	// deleting the agent ends its tokens and its key
	if err := s.DeleteAgent("claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate(secret); ok {
		t.Error("a deleted agent's token works")
	}
	if _, ok := s.PrincipalBySSHKey("SHA256:one"); ok {
		t.Error("a deleted agent's key works")
	}
	if len(s.AgentTokens("claude-code")) != 0 {
		t.Error("the tokens outlive the agent")
	}

	// people and tokens by key
	if p, ok := s.Principal("user:alice"); !ok || p.Name() != "alice" || !p.Has("accounts:write") || !p.Person() {
		t.Errorf("a person: %+v", p)
	}
	if p, ok := s.Principal("token:legacy"); !ok || p.Name() != "legacy" {
		t.Errorf("a token: %+v", p)
	}
	for _, bad := range []string{"", "user:", "user:nobody", "token:nobody", "agent:nobody", "machine:x"} {
		if _, ok := s.Principal(bad); ok {
			t.Errorf("%q resolves", bad)
		}
	}
}

func TestClaimToken(t *testing.T) {
	s, _ := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "carol", "oncall")
	secret, legacy, _ := s.CreateToken("claude-code", []string{"status:read", "sessions:read", "alerts:write", "machines:ssh"}, 30*24*time.Hour, "cli:root")
	_, _ = s.UpdateToken("claude-code", func(t *Token) error {
		t.Machines = []string{"edge-1"}
		t.SSHKeys = []SSHKey{{Key: "ssh-ed25519 AAAA", Fingerprint: "SHA256:k"}}
		return nil
	})
	machine, _, _ := s.CreateSubjectToken("probe-edge-1", "edge-1", []string{"probe:write"}, 24*time.Hour, "cli:root")
	_ = machine
	if !Legacy(legacy) {
		t.Fatal("not legacy")
	}

	// before any claim it works as it always did
	if p, ok := s.Authenticate(secret); !ok || p.Name() != "claude-code" || !p.Has("machines:ssh") {
		t.Fatalf("legacy: %+v %v", p, ok)
	}
	if _, _, err := s.ClaimToken("probe-edge-1", "alice", "", "", "alice"); err == nil {
		t.Error("a machine's token claimed")
	}
	a, tok, err := s.ClaimToken("claude-code", "carol", "", "", "alice")
	if err != nil || a.Name != "claude-code" || a.Owner != "carol" || a.Preset != "oncall" || !slices.Equal(a.Machines, []string{"edge-1"}) ||
		len(a.SSHKeys) != 1 || tok.Agent != "claude-code" || len(tok.Scopes) != 0 || len(tok.SSHKeys) != 0 || !tok.Expires.Equal(legacy.Expires) {
		t.Fatalf("claim: %+v %+v %v", a, tok, err)
	}
	p, ok := s.Authenticate(secret)
	if !ok || p.Name() != "carol / claude-code" || !p.Has("machines:ssh") || !p.Reaches("edge-1") || p.Reaches("edge-2") {
		t.Errorf("claimed: %+v %v", p, ok)
	}
	if p, ok := s.PrincipalBySSHKey("SHA256:k"); !ok || p.Agent != "claude-code" {
		t.Errorf("the key moved: %+v %v", p, ok)
	}
	if _, _, err := s.ClaimToken("claude-code", "carol", "", "", "alice"); err == nil {
		t.Error("claimed twice")
	}

	// a custom token stays custom, and its owner's role still limits it
	secret, _, _ = s.CreateToken("ops-bot", []string{"status:read", "machines:ssh", "config:write"}, 30*24*time.Hour, "cli:root")
	a, _, err = s.ClaimToken("ops-bot", "carol", "", "", "alice")
	if err != nil || a.Preset != PresetCustom || !slices.Equal(a.Scopes, []string{"config:write", "machines:ssh", "status:read"}) {
		t.Fatalf("custom claim: %+v %v", a, err)
	}
	if p, _ := s.Authenticate(secret); p.Has("config:write") || !p.Has("machines:ssh") {
		t.Errorf("custom within oncall: %v", p.Scopes)
	}
	// into another person's agent: refused
	_, _, _ = s.CreateToken("third", []string{"status:read"}, 24*time.Hour, "cli:root")
	if _, _, err := s.ClaimToken("third", "alice", "claude-code", "", "alice"); !errors.Is(err, ErrExists) {
		t.Errorf("into carol's agent for alice: %v", err)
	}
	// into carol's own agent: only when that widens nothing
	if _, _, err := s.ClaimToken("third", "carol", "claude-code", "", "alice"); err == nil || !strings.Contains(err.Error(), "may do more") {
		t.Errorf("a status:read token into an on-call agent: %v", err)
	}
	_, _, _ = s.CreateToken("fourth", []string{"status:read", "sessions:read", "alerts:write", "machines:ssh"}, 24*time.Hour, "cli:root")
	_, _ = s.UpdateToken("fourth", func(t *Token) error { t.Machines = []string{"edge-1", "edge-2"}; return nil })
	if _, _, err := s.ClaimToken("fourth", "carol", "claude-code", "", "alice"); err != nil {
		t.Errorf("into carol's own agent: %v", err)
	}
	if got := s.AgentTokens("claude-code"); len(got) != 2 {
		t.Errorf("tokens of claude-code: %v", got)
	}
}

func TestGrants(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	if _, err := s.CreateAgent(Agent{Name: "claude-code", Owner: "alice", Preset: "oncall", Client: "lbc_x"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.RegisterClient(OAuthClient{Name: "Claude Code", RedirectURIs: []string{"http://localhost:5555/callback"}})
	if err != nil || !strings.HasPrefix(c.ID, "lbc_") {
		t.Fatalf("register: %+v %v", c, err)
	}
	if _, ok := s.OAuthClient(c.ID); !ok {
		t.Fatal("the client is not found")
	}

	in, err := s.CreateGrant(Grant{Agent: "claude-code", Client: c.ID, ClientName: "Claude Code", Scope: "oncall offline_access", Expires: now.Add(30 * 24 * time.Hour), Code: "codehash"}, true)
	if err != nil || !strings.HasPrefix(in.Access, AccessPrefix) || !strings.HasPrefix(in.Refresh, RefreshPrefix) || !in.AccessExpires.Equal(now.Add(time.Hour)) {
		t.Fatalf("grant: %+v %v", in, err)
	}
	p, ok := s.Authenticate(in.Access)
	if !ok || p.Name() != "alice / claude-code" || p.Grant != in.Grant.ID || p.Client != "Claude Code" || !p.Has("machines:ssh") || p.Has("machines:write") {
		t.Fatalf("access: %+v %v", p, ok)
	}
	if p, ok := s.AgentPrincipal("claude-code"); !ok || !p.Expires.Equal(now.Add(30*24*time.Hour)) {
		t.Errorf("the agent lives as long as its grant: %+v %v", p, ok)
	}

	// refresh: rotation, and another client cannot use it
	if _, err := s.RefreshGrant(in.Refresh, "lbc_other"); !errors.Is(err, ErrBadGrant) {
		t.Errorf("another client's refresh: %v", err)
	}
	*now = now.Add(2 * time.Hour)
	if _, ok := s.Authenticate(in.Access); ok {
		t.Error("an access token outlives its hour")
	}
	next, err := s.RefreshGrant(in.Refresh, c.ID)
	if err != nil || next.Refresh == in.Refresh || next.Access == in.Access {
		t.Fatalf("refresh: %+v %v", next, err)
	}
	if _, ok := s.Authenticate(next.Access); !ok {
		t.Error("the new access token does not work")
	}
	// the replaced refresh token used again: the grant is revoked, the new tokens with it
	if _, err := s.RefreshGrant(in.Refresh, c.ID); !errors.Is(err, ErrReplayed) {
		t.Errorf("reuse: %v", err)
	}
	if _, ok := s.Authenticate(next.Access); ok {
		t.Error("tokens outlive a replayed refresh token")
	}
	if _, err := s.RefreshGrant(next.Refresh, c.ID); !errors.Is(err, ErrBadGrant) {
		t.Errorf("the revoked grant refreshed: %v", err)
	}

	// revocation (RFC 7009): any of a grant's tokens ends the grant, as a client signing out means
	in, _ = s.CreateGrant(Grant{Agent: "claude-code", Client: c.ID, ClientName: "Claude Code", Expires: now.Add(24 * time.Hour)}, true)
	if _, ok := s.RevokeOAuthToken(in.Access, "lbc_other"); ok {
		t.Error("another client revoked a token")
	}
	if _, ok := s.RevokeOAuthToken(in.Access, c.ID); !ok {
		t.Error("revoke access")
	}
	if _, ok := s.Authenticate(in.Access); ok {
		t.Error("a revoked access token works")
	}
	if _, err := s.RefreshGrant(in.Refresh, c.ID); !errors.Is(err, ErrBadGrant) {
		t.Errorf("the grant outlives revoking its access token: %v", err)
	}
	in, _ = s.CreateGrant(Grant{Agent: "claude-code", Client: c.ID, ClientName: "Claude Code", Expires: now.Add(24 * time.Hour)}, true)
	g := s.Grants("claude-code")
	if len(g) != 1 {
		t.Fatalf("grants: %+v", g)
	}
	if err := s.RevokeGrant(g[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Grants("")) != 0 {
		t.Error("the grant outlives revoking it")
	}

	// a replayed authorization code revokes what it gave
	in, _ = s.CreateGrant(Grant{Agent: "claude-code", Client: c.ID, Expires: now.Add(24 * time.Hour), Code: "replayed"}, false)
	if in.Refresh != "" {
		t.Error("a refresh token without offline_access")
	}
	if _, ok := s.RevokeCode("replayed"); !ok {
		t.Error("RevokeCode")
	}
	if _, ok := s.Authenticate(in.Access); ok {
		t.Error("tokens of a replayed code work")
	}

	// deleting the agent ends its grants; a disabled owner's grants do not work
	in, _ = s.CreateGrant(Grant{Agent: "claude-code", Client: c.ID, Expires: now.Add(24 * time.Hour)}, true)
	invite(t, s, "dave", "owner")
	_, _ = s.UpdateAccount("alice", func(a *Account) error { a.Disabled = true; return nil })
	if _, ok := s.Authenticate(in.Access); ok {
		t.Error("an access token of a disabled owner's agent works")
	}
	if _, err := s.RefreshGrant(in.Refresh, c.ID); !errors.Is(err, ErrBadGrant) {
		t.Errorf("a disabled owner's grant refreshed: %v", err)
	}
	if err := s.DeleteAgent("claude-code"); err != nil {
		t.Fatal(err)
	}
	if len(s.Grants("")) != 0 {
		t.Error("grants outlive their agent")
	}

	// registrations never used lapse
	*now = now.Add(ClientUnused + time.Hour)
	if _, ok := s.OAuthClient(c.ID); ok {
		t.Error("an unused registration outlives ClientUnused")
	}
	used, _ := s.RegisterClient(OAuthClient{Name: "Codex"})
	s.UseClient(used.ID)
	*now = now.Add(ClientUnused + time.Hour)
	if _, ok := s.OAuthClient(used.ID); !ok {
		t.Error("a used registration lapsed")
	}
}

// TestGrantCeilings: a grant may do what the person allowed it, within what its agent may; and the keys an
// agent added with a credential go with that credential.
func TestGrantCeilings(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	if _, err := s.CreateAgent(Agent{Name: "claude-code", Owner: "alice", Preset: "operator"}); err != nil {
		t.Fatal(err)
	}
	narrow, _ := s.CreateGrant(Grant{Agent: "claude-code", Client: "c1", Preset: "readonly", Machines: []string{"edge-1"}, Expires: now.Add(24 * time.Hour)}, true)
	wide, _ := s.CreateGrant(Grant{Agent: "claude-code", Client: "c2", Preset: "operator", Expires: now.Add(24 * time.Hour)}, true)
	p, ok := s.Authenticate(narrow.Access)
	if !ok || p.Has("alerts:write") || !slices.Equal(p.Machines, []string{"edge-1"}) {
		t.Errorf("the narrow grant: %+v", p)
	}
	if q, ok := s.PrincipalWith(p.Key(), p.Cred()); !ok || q.Has("alerts:write") || !slices.Equal(q.Machines, []string{"edge-1"}) {
		t.Errorf("the narrow grant, by key and credential: %+v %v", q, ok)
	}
	if q, ok := s.PrincipalWith("agent:other", p.Cred()); ok {
		t.Errorf("a grant resolves for another agent: %+v", q)
	}
	if p, _ := s.Authenticate(wide.Access); !p.Has("machines:write") || len(p.Machines) != 0 {
		t.Errorf("the wide grant: %+v", p)
	}
	// the agent narrowed to another machine: the narrow grant reaches nothing
	_, _ = s.UpdateAgent("claude-code", func(a *Agent) error { a.Machines = []string{"edge-2"}; return nil })
	if p, _ := s.Authenticate(narrow.Access); p.Has("machines:ssh") || p.Reaches("edge-1") || p.Reaches("edge-2") {
		t.Errorf("a grant whose machines the agent lost: %+v", p)
	}
	if p, _ := s.Authenticate(wide.Access); !p.Reaches("edge-2") || p.Reaches("edge-1") {
		t.Errorf("the wide grant within the agent's machines: %+v", p)
	}
	_, _ = s.UpdateAgent("claude-code", func(a *Agent) error { a.Machines = nil; return nil })

	// keys an agent added with a credential live and die with it
	_, tok, _ := s.CreateAgentToken("claude-code", 24*time.Hour, "alice")
	byGrant := SSHKey{Key: "ssh-ed25519 AAAA1", Fingerprint: "SHA256:g", Via: "grant:" + narrow.Grant.ID}
	byToken := SSHKey{Key: "ssh-ed25519 AAAA2", Fingerprint: "SHA256:t", Via: "token:" + tok.Name}
	byPerson := SSHKey{Key: "ssh-ed25519 AAAA3", Fingerprint: "SHA256:p"}
	for _, k := range []SSHKey{byGrant, byToken, byPerson} {
		if _, err := s.AddAgentSSHKey("claude-code", k); err != nil {
			t.Fatal(err)
		}
	}
	for _, fp := range []string{"SHA256:g", "SHA256:t", "SHA256:p"} {
		if _, ok := s.PrincipalBySSHKey(fp); !ok {
			t.Errorf("key %s does not sign in", fp)
		}
	}
	// a key added through the narrow (read-only) grant may do what that grant may, no more: not even reach a machine
	if p, _ := s.PrincipalBySSHKey("SHA256:g"); p.Has("alerts:write") || p.Has("machines:ssh") || !slices.Equal(p.Machines, []string{"edge-1"}) {
		t.Errorf("the narrow grant's key: %+v", p)
	}
	if p, _ := s.PrincipalWith("agent:claude-code", "key:SHA256:g"); p.Has("alerts:write") || p.Reaches("edge-2") {
		t.Errorf("the narrow grant's key, rechecked: %+v", p)
	}
	if p, _ := s.PrincipalBySSHKey("SHA256:p"); !p.Has("machines:write") || !p.Reaches("edge-2") {
		t.Errorf("the person's key: %+v", p)
	}
	if err := s.RevokeGrant(narrow.Grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(tok.Name); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Agent("claude-code")
	if len(a.SSHKeys) != 1 || a.SSHKeys[0].Fingerprint != "SHA256:p" {
		t.Errorf("the keys after revoking their credentials: %+v", a.SSHKeys)
	}
	if p, ok := s.PrincipalBySSHKey("SHA256:p"); !ok || !s.CredentialLive("key:SHA256:p", p) {
		t.Error("the person's key stopped with the agent's other credentials alive")
	}
	// a key whose credential expired stops signing in, though it is still listed
	short, _ := s.CreateGrant(Grant{Agent: "claude-code", Client: "c3", Preset: "readonly", Expires: now.Add(time.Hour)}, true)
	_, _ = s.AddAgentSSHKey("claude-code", SSHKey{Key: "ssh-ed25519 AAAA4", Fingerprint: "SHA256:s", Via: "grant:" + short.Grant.ID})
	*now = now.Add(2 * time.Hour)
	if _, ok := s.PrincipalBySSHKey("SHA256:s"); ok {
		t.Error("a key of an expired grant signs in")
	}
	if _, ok := s.PrincipalBySSHKey("SHA256:p"); !ok {
		t.Error("the person's key stopped while the wide grant lives")
	}
}
