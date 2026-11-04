package store

// The pair's shared database, on the token cache: one instance's cache is a snapshot of its open,
// so what the other made arrives by RefreshTokens — the standby that takes over verifies the
// primary's tokens at once. (PostgreSQL when the environment names one; a second open of one
// SQLite file is the lock's to refuse, so there the test skips.)

import (
	"testing"
	"time"
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
	two.RefreshTokens()
	got, ok := two.VerifyToken(secret)
	if !ok || got.Name != "made-by-the-primary" {
		t.Fatalf("after the refresh: %v %v", got, ok)
	}
	if _, ok := one.VerifyToken(secret); !ok {
		t.Fatal("the maker's own cache lost it")
	}
}
