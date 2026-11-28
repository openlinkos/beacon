package store

import (
	"testing"
	"time"
)

// TestRetiredSecretsStayIssued checks that a token's secret still counts as issued for RetiredFor after
// it was revoked, replaced by enroll -rotate or a renewal, or expired, across a restart, and is never
// accepted; and that a secret Beacon never issued does not.
func TestRetiredSecretsStayIssued(t *testing.T) {
	s, path := open(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s.SetClock(clock)

	revoked, _, err := s.CreateSubjectToken("host-edge-1", "edge-1", []string{"host:write"}, 365*24*time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken("host-edge-1"); err != nil {
		t.Fatal(err)
	}
	rotated, _, err := s.CreateSubjectToken("host-edge-1", "edge-1", []string{"host:write"}, 365*24*time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	// enroll -rotate replaces the token in one write
	fresh, _, err := s.CreateSubjectTokenRecorded("host-edge-1", "edge-1", []string{"host:write"}, 365*24*time.Hour, "cli:root", true,
		func(*Token, Token) []AuditEntry { return nil })
	if err != nil {
		t.Fatal(err)
	}
	renewed, _, err := s.CreateToken("renewing", []string{"status:read"}, 30*24*time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RenewToken("renewing"); err != nil {
		t.Fatal(err)
	}
	expiring, _, err := s.CreateToken("short", []string{"status:read"}, time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	agent := createAgentToken(t, s)
	if err := s.DeleteAgent("retired-agent"); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour) // past the renewal's grace and the short token's expiry
	check := func(s *Store, when string, want bool) {
		t.Helper()
		for name, secret := range map[string]string{"revoked": revoked, "rotated": rotated, "renewed": renewed, "expired": expiring, "deleted agent's": agent} {
			if got := s.Issued(secret); got != want {
				t.Errorf("%s: the %s secret counts as issued: %v", when, name, got)
			}
			if _, ok := s.VerifyToken(secret); ok {
				t.Errorf("%s: the %s secret is accepted", when, name)
			}
		}
		if !s.Issued(fresh) {
			t.Errorf("%s: the new token is not issued", when)
		}
		if s.Issued("lbt_neverissued") {
			t.Errorf("%s: a secret never issued counts as issued", when)
		}
	}
	check(s, "after the changes", true)

	_ = s.Close()
	s, err = reopen(t, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.SetClock(clock)
	check(s, "after a restart", true)
	if err := s.RefreshTokens(); err != nil {
		t.Fatal(err)
	}
	check(s, "after a refresh", true)

	now = now.Add(RetiredFor)
	check(s, "a week later", false)
}

// createAgentToken makes an agent called retired-agent for a new owner, and a token of it.
func createAgentToken(t *testing.T, s *Store) string {
	t.Helper()
	owner(t, s, "olive")
	if _, err := s.CreateAgent(Agent{Name: "retired-agent", Owner: "olive", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := s.CreateAgentToken("retired-agent", 30*24*time.Hour, "olive")
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// TestEndedSignInsStayIssued checks that the access and refresh tokens of an OAuth sign-in still count as
// issued for RetiredFor after the sign-in ends (revoked, ended by a new password, or with its agent) and
// after an access token expires, across a restart, and are never accepted; and that OAuth tokens Beacon
// never issued do not.
func TestEndedSignInsStayIssued(t *testing.T) {
	s, path := open(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s.SetClock(clock)
	owner(t, s, "oscar")
	invite(t, s, "olive", "oncall")
	for agent, owner := range map[string]string{"revoked": "oscar", "reset": "olive", "deleted": "oscar", "expiring": "oscar"} {
		if _, err := s.CreateAgent(Agent{Name: agent, Owner: owner, Preset: "readonly"}); err != nil {
			t.Fatal(err)
		}
	}
	signIn := func(agent string) Issued {
		t.Helper()
		in, err := s.CreateGrant(Grant{Agent: agent, Client: "lbc_" + agent, ClientName: agent, Preset: "readonly", Expires: now.Add(30 * 24 * time.Hour)}, true)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	revoked, reset, deleted, expired := signIn("revoked"), signIn("reset"), signIn("deleted"), signIn("expiring")
	if err := s.RevokeGrant(revoked.Grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword("olive", "another good password", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAgent("deleted"); err != nil {
		t.Fatal(err)
	}
	if gs := s.Grants(""); len(gs) != 1 || gs[0].Agent != "expiring" {
		t.Fatalf("the sign-ins left: %+v", gs)
	}
	// the expiring sign-in goes on: its first access token expires, and a refresh forgets what expired
	now = now.Add(2 * AccessTTL)
	if _, err := s.RefreshGrant(expired.Refresh, "lbc_expiring"); err != nil {
		t.Fatal(err)
	}

	secrets := map[string]string{
		"revoked access": revoked.Access, "revoked refresh": revoked.Refresh,
		"reset access": reset.Access, "reset refresh": reset.Refresh,
		"deleted agent's access": deleted.Access, "deleted agent's refresh": deleted.Refresh,
		"expired access": expired.Access,
	}
	check := func(s *Store, when string, want bool) {
		t.Helper()
		for name, secret := range secrets {
			if got := s.Issued(secret); got != want {
				t.Errorf("%s: the %s token counts as issued: %v", when, name, got)
			}
			if _, ok := s.Authenticate(secret); ok {
				t.Errorf("%s: the %s token is accepted", when, name)
			}
		}
		if _, err := s.RefreshGrant(reset.Refresh, "lbc_reset"); err == nil {
			t.Errorf("%s: an ended sign-in's refresh token refreshes", when)
		}
		for _, never := range []string{AccessPrefix + "neverissued", RefreshPrefix + "neverissued"} {
			if s.Issued(never) {
				t.Errorf("%s: %s counts as issued", when, never)
			}
		}
	}
	check(s, "after the sign-ins ended", true)
	_ = s.Close()
	s, err := reopen(t, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.SetClock(clock)
	check(s, "after a restart", true)

	now = now.Add(RetiredFor)
	check(s, "a week later", false)
}
