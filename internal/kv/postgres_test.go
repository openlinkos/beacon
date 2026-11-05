package kv

// The PostgreSQL backend's own tests. The contract (Get, Put, Range, sequences,
// compare-and-swap, transactions) is tested once on whatever backend openAny picks — CI runs the
// suite on SQLite and on PostgreSQL both — so what is left here is the backend's own shape: what a
// bad DSN says, that a DSN's secret never reaches an error, and what the command line's checks see.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
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

// TestPostgresViewIsASnapshot: a View is Repeatable Read, so a second statement in it does not
// see a write that committed after it began.
func TestPostgresViewIsASnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode skips the database tests")
	}
	db, err := OpenPostgres(PostgresOptions{DSN: testDatabase(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Update(func(tx Tx) error {
		return tx.Put([]byte("b"), []byte("k"), []byte("v1"))
	}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	saw := make(chan []byte, 1)
	go func() {
		_ = db.View(func(tx Tx) error {
			first := tx.Get([]byte("b"), []byte("k"))
			close(started)
			time.Sleep(200 * time.Millisecond)
			saw <- append([]byte(nil), tx.Get([]byte("b"), []byte("k"))...)
			if !bytes.Equal(first, []byte("v1")) {
				t.Errorf("first Get: %q", first)
			}
			return nil
		})
	}()
	<-started
	if err := db.Update(func(tx Tx) error {
		return tx.Put([]byte("b"), []byte("k"), []byte("v2"))
	}); err != nil {
		t.Fatal(err)
	}
	got := <-saw
	if !bytes.Equal(got, []byte("v1")) {
		t.Fatalf("View saw the concurrent write: %q", got)
	}
}

// TestPostgresWriteGuard: a guard that refuses the write rolls it back.
func TestPostgresWriteGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode skips the database tests")
	}
	db, err := OpenPostgres(PostgresOptions{DSN: testDatabase(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetWriteGuard(func(context.Context, *sql.Tx) error { return errors.New("blocked") })
	err = db.Update(func(tx Tx) error { return tx.Put([]byte("b"), []byte("k"), []byte("v")) })
	if err == nil || err.Error() != "blocked" {
		t.Fatalf("guard: %v", err)
	}
	db.SetWriteGuard(nil)
	_ = db.View(func(tx Tx) error {
		if v := tx.Get([]byte("b"), []byte("k")); v != nil {
			t.Errorf("a guarded write left %q", v)
		}
		return nil
	})
}
