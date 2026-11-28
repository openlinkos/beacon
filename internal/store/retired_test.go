package store

import (
	"testing"
	"time"
)

// TestRetiredSecretsStayIssued checks that a token's secret still counts as issued for RetiredFor after
// it was revoked (as enroll -rotate does), replaced by a renewal or expired, across a restart, and is
// never accepted; and that a secret Beacon never issued does not.
func TestRetiredSecretsStayIssued(t *testing.T) {
	s, path := open(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s.SetClock(clock)

	rotated, _, err := s.CreateSubjectToken("host-edge-1", "edge-1", []string{"host:write"}, 365*24*time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken("host-edge-1"); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := s.CreateSubjectToken("host-edge-1", "edge-1", []string{"host:write"}, 365*24*time.Hour, "cli:root")
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
		for name, secret := range map[string]string{"rotated": rotated, "renewed": renewed, "expired": expiring, "deleted agent's": agent} {
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
