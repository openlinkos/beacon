package kv

// The PostgreSQL backend's own tests. The contract (Get, Put, Range, sequences,
// compare-and-swap, transactions) is tested once on whatever backend openAny picks — CI runs the
// suite on SQLite and on PostgreSQL both — so what is left here is the backend's own shape: what a
// bad DSN says, that a DSN's secret never reaches an error, and what the command line's checks see.

import (
	"strings"
	"testing"
)

func TestPostgresNeedsDSN(t *testing.T) {
	if _, err := OpenPostgres(PostgresOptions{}); err == nil || !strings.Contains(err.Error(), "DSN") {
		t.Fatalf("no DSN: %v", err)
	}
}

func TestPostgresBadDSN(t *testing.T) {
	if _, err := OpenPostgres(PostgresOptions{DSN: "not a dsn ="}); err == nil {
		t.Fatal("a bad DSN should fail")
	}
}

func TestPostgresUnreachable(t *testing.T) {
	// a port nothing listens on, so the failure is the connection's, quickly
	_, err := OpenPostgres(PostgresOptions{DSN: "postgres://nobody:secret@127.0.0.1:1/beacon?sslmode=disable&connect_timeout=2"})
	if err == nil {
		t.Fatal("an unreachable server should fail")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("the error carries the DSN's password: %v", err)
	}
}

// TestPostgresHealthWhere: the checks the command line runs against a live database.
func TestPostgresHealthWhere(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode skips the database tests")
	}
	db, err := OpenPostgres(PostgresOptions{DSN: testDatabase(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if h, err := db.Health(); err != nil || h != "ok" {
		t.Fatalf("health: %q %v", h, err)
	}
	if w := db.Where(); !strings.HasPrefix(w, "postgres ") || strings.Contains(w, "password") {
		t.Fatalf("where: %q", w)
	}
}
