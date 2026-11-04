package store

// The move from SQLite to PostgreSQL, tested against a live PostgreSQL when
// BEACON_TEST_POSTGRES_DSN names one (CI's service container): a store with groups, tokens,
// accounts and audit entries moves in one piece, the counts and the audit chain verify on the far
// side, and a second move over a full database copies nothing.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateToPostgres(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server this test moves into")
	}
	path := filepath.Join(t.TempDir(), "s.db") // the SQLite source this test fills and then moves
	src, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	src.now = func() time.Time { return now }
	for i := range 5 {
		if err := src.PutGroup(Group{ID: string(rune('a' + i)), Status: "firing", StartsAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := src.CreateToken("probe-edge", []string{"status:read"}, 24*time.Hour, "cli:test"); err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		if err := src.Audit(AuditEntry{Actor: "alice", Via: "api", Action: "test", Detail: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.CheckpointAudit(); err != nil {
		t.Fatal(err)
	}
	want, _, err := src.AuditQuery(AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	groups, tokens := len(src.Groups()), len(src.Tokens())
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}

	dst := Options{Driver: "postgres", DSN: testDatabase(t, os.Getenv("BEACON_TEST_POSTGRES_DSN"))}
	moved, err := MigrateToPostgres(Options{Path: path}, dst)
	if err != nil {
		t.Fatalf("the move: %v", err)
	}
	if !moved {
		t.Fatal("nothing was moved")
	}

	// the far side: what moved reads back, and the chain verifies
	s, err := OpenWith(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := len(s.Groups()); got != groups {
		t.Errorf("groups: %d of %d", got, groups)
	}
	if got := len(s.Tokens()); got != tokens {
		t.Errorf("tokens: %d of %d", got, tokens)
	}
	if got, _, err := s.AuditQuery(AuditFilter{}); err != nil || len(got) != len(want)+1 { // the genesis record
		t.Fatalf("audit: %d of %d (err %v)", len(got), len(want)+1, err)
	}
	if res, err := s.VerifyAudit(); err != nil || res.Events != uint64(len(want)+1) {
		t.Fatalf("the chain on the far side: %v %+v", err, res)
	}
	if _, ok := s.Group("c"); !ok {
		t.Error("a group the source held is gone")
	}

	// a full destination copies nothing
	if moved, err := MigrateToPostgres(Options{Path: path}, dst); err != nil || moved {
		t.Fatalf("the second move: moved=%v err=%v", moved, err)
	}
}

// TestMigrateToPostgresBolt: the bbolt migration lands in PostgreSQL as it does in SQLite.
func TestMigrateToPostgresBolt(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server this test moves into")
	}
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "status.db")
	fixture(t, boltPath)
	dst := Options{Driver: "postgres", DSN: testDatabase(t, os.Getenv("BEACON_TEST_POSTGRES_DSN"))}
	migrated, err := Migrate(boltPath, dst)
	if err != nil {
		t.Fatalf("the move: %v", err)
	}
	if !migrated {
		t.Fatal("nothing was moved")
	}
	s, err := OpenWith(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if res, err := s.VerifyAudit(); err != nil {
		t.Fatalf("the chain on the far side: %v (res %+v)", err, res)
	}
	if got := len(s.Groups()); got == 0 {
		t.Error("no groups came over")
	}
}
