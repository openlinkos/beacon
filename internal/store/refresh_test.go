package store

// The pair's shared database, on the token cache: one instance's cache is a snapshot of its open,
// so what the other made arrives by RefreshTokens — the standby that takes over verifies the
// primary's tokens at once. (PostgreSQL when the environment names one; a second open of one
// SQLite file is the lock's to refuse, so there the test skips.)

import (
	"database/sql"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

func TestRefreshTokens(t *testing.T) {
	one, path := open(t)
	var two *Store
	var err error
	if currentDSN != "" {
		two, err = OpenWith(Options{Driver: "postgres", DSN: currentDSN})
	} else {
		two, err = Open(path) // the file backend's single writer: refused while one holds it
	}
	if err != nil {
		t.Skipf("the second open of the same database: %v", err)
	}
	defer func() { _ = two.Close() }()
	secret, _, err := one.CreateToken("made-by-the-primary", []string{"status:read"}, 24*time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := two.VerifyToken(secret); ok {
		t.Fatal("the snapshot verified a token made after its open")
	}
	if err := two.RefreshTokens(); err != nil {
		t.Fatal(err)
	}
	got, ok := two.VerifyToken(secret)
	if !ok || got.Name != "made-by-the-primary" {
		t.Fatalf("after the refresh: %v %v", got, ok)
	}
	if _, ok := one.VerifyToken(secret); !ok {
		t.Fatal("the maker's own cache lost it")
	}
}

// TestRefreshTokensKeepsTheTableWhenTheReadFails: a failed Range must not swap in an empty cache.
func TestRefreshTokensKeepsTheTableWhenTheReadFails(t *testing.T) {
	s, _ := open(t)
	secret, _, err := s.CreateToken("keep-me", []string{"status:read"}, 24*time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Storage().UpdateBoth(func(_ kv.Tx, raw *sql.Tx) error {
		_, err := raw.Exec(`ALTER TABLE kv RENAME TO kv_hidden`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Storage().UpdateBoth(func(_ kv.Tx, raw *sql.Tx) error {
			_, err := raw.Exec(`ALTER TABLE kv_hidden RENAME TO kv`)
			return err
		})
	})
	if err := s.RefreshTokens(); err == nil {
		t.Fatal("refresh should fail when the table is gone")
	}
	if _, ok := s.VerifyToken(secret); !ok {
		t.Fatal("a failed refresh dropped the cache")
	}
}

// TestRenewTokenDoesNotResurrectARevokedToken: a standby that still has the token in cache
// must not write it back after the primary revoked it.
func TestRenewTokenDoesNotResurrectARevokedToken(t *testing.T) {
	one, path := open(t)
	var two *Store
	var err error
	if currentDSN != "" {
		two, err = OpenWith(Options{Driver: "postgres", DSN: currentDSN})
	} else {
		two, err = Open(path)
	}
	if err != nil {
		t.Skipf("the second open of the same database: %v", err)
	}
	defer func() { _ = two.Close() }()
	secret, _, err := one.CreateToken("revoked-then-renewed", []string{"status:read"}, 24*time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := two.RefreshTokens(); err != nil {
		t.Fatal(err)
	}
	if err := one.RevokeToken("revoked-then-renewed"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := two.RenewToken("revoked-then-renewed"); err == nil {
		t.Fatal("renewed a token the other instance revoked")
	}
	if _, err := two.UpdateToken("revoked-then-renewed", func(t *Token) error {
		t.Machines = []string{"edge-1"}
		return nil
	}); err == nil {
		t.Fatal("updated a token the other instance revoked")
	}
	if err := two.RefreshTokens(); err != nil {
		t.Fatal(err)
	}
	if _, ok := two.VerifyToken(secret); ok {
		t.Fatal("the revoked token was written back")
	}
	if _, ok := one.VerifyToken(secret); ok {
		t.Fatal("the revoked token works on the instance that revoked it")
	}
}
