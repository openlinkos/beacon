package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestRenewToken(t *testing.T) {
	s, path := open(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	first, tok, err := s.CreateToken("claude-code", []string{"status:read", "machines:ssh"}, 30*24*time.Hour, "cli:root")
	if err != nil || tok.Lifetime != 30*24*time.Hour || !tok.Renewable() || !tok.RenewableAt().Equal(now) {
		t.Fatalf("created: %+v %v", tok, err)
	}
	if _, err := s.UpdateToken("claude-code", func(t *Token) error {
		t.SSHKeys = []SSHKey{{Key: "ssh-ed25519 AAAA", Fingerprint: "SHA256:k1"}}
		t.Machines = []string{"edge-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(20 * 24 * time.Hour)
	second, renewed, err := s.RenewToken("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if second == first || renewed.Hash == tok.Hash || renewed.OldHash != tok.Hash || !renewed.Expires.Equal(now.Add(30*24*time.Hour)) ||
		!renewed.Renewed.Equal(now) || !renewed.OldUntil.Equal(now.Add(RenewGrace)) || renewed.Lifetime != tok.Lifetime {
		t.Fatalf("renewed: %+v", renewed)
	}
	if len(renewed.SSHKeys) != 1 || len(renewed.Machines) != 1 || renewed.Subject != "" || len(renewed.Scopes) != 2 || !renewed.Created.Equal(tok.Created) {
		t.Errorf("identity kept: %+v", renewed)
	}
	if got, ok := s.VerifyToken(second); !ok || got.Name != "claude-code" {
		t.Fatalf("the new secret: %+v %v", got, ok)
	}
	if got, ok := s.VerifyToken(first); !ok || got.Hash != renewed.Hash {
		t.Fatalf("the old secret during the grace period: %+v %v", got, ok)
	}
	if got, ok := s.TokenBySSHKey("SHA256:k1"); !ok || got.Hash != renewed.Hash || !got.Expires.Equal(renewed.Expires) {
		t.Errorf("SSH sign-in sees the renewed token: %+v %v", got, ok)
	}
	if list := s.Tokens(); len(list) != 1 {
		t.Errorf("listed once: %+v", list)
	}
	if _, _, err := s.RenewToken("claude-code"); !errors.Is(err, ErrTooSoon) {
		t.Errorf("renewing again at once: %v", err)
	}
	if at := renewed.RenewableAt(); !at.Equal(now.Add(RenewEvery)) {
		t.Errorf("RenewableAt: %v", at)
	}

	// the grace period survives a restart and then ends
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s2.now = func() time.Time { return now.Add(RenewGrace - time.Second) }
	if _, ok := s2.VerifyToken(first); !ok {
		t.Error("the old secret is lost on a restart within the grace period")
	}
	s2.now = func() time.Time { return now.Add(RenewGrace) }
	if _, ok := s2.VerifyToken(first); ok {
		t.Error("the old secret outlived the grace period")
	}
	if _, ok := s2.VerifyToken(second); !ok {
		t.Error("the new secret stopped working")
	}
	s2.now = func() time.Time { return now.Add(RenewEvery) }
	if _, _, err := s2.RenewToken("claude-code"); err != nil {
		t.Errorf("renewing after a day: %v", err)
	}
	if _, ok := s2.VerifyToken(second); !ok {
		t.Error("the second secret must still work in its grace period")
	}
	if err := s2.RevokeToken("claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.VerifyToken(second); ok {
		t.Error("a revoked token's old secret verified")
	}
	if _, _, err := s2.RenewToken("claude-code"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renewing a revoked token: %v", err)
	}
}

func TestRenewTokenRefusesMachines(t *testing.T) {
	s, _ := open(t)
	for _, tc := range []struct {
		name, subject string
		scopes        []string
	}{{"probe-edge-1", "edge-1", []string{"probe:write"}}, {"host-worker", "worker", []string{"host:write"}}, {"heartbeat-nas", "", []string{"heartbeat:write"}}} {
		if _, tok, err := s.CreateSubjectToken(tc.name, tc.subject, tc.scopes, time.Hour, "cli:root"); err != nil || tok.Renewable() {
			t.Fatalf("%s: %+v %v", tc.name, tok, err)
		}
		if _, _, err := s.RenewToken(tc.name); !errors.Is(err, ErrNotRenewable) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if _, _, err := s.RenewToken("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: %v", err)
	}
}

// TestLifetimeDerived loads a token stored before Lifetime was kept: its lifetime is what it was
// created with, Expires - Created, and a renewal extends it by that much.
func TestLifetimeDerived(t *testing.T) {
	s, path := open(t)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := Token{Name: "legacy", Hash: hashToken("lbt_legacy"), Scopes: []string{"status:read"}, Created: created, Expires: created.Add(90 * 24 * time.Hour)}
	b, _ := json.Marshal(old)
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketTokens).Put([]byte(old.Name), b) }); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	now := created.Add(80 * 24 * time.Hour)
	s2.now = func() time.Time { return now }
	got, ok := s2.VerifyToken("lbt_legacy")
	if !ok || got.Lifetime != 90*24*time.Hour {
		t.Fatalf("derived lifetime: %+v %v", got, ok)
	}
	if _, renewed, err := s2.RenewToken("legacy"); err != nil || !renewed.Expires.Equal(now.Add(90*24*time.Hour)) {
		t.Fatalf("renewed by the derived lifetime: %+v %v", renewed, err)
	}
}
