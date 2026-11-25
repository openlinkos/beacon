package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPostgresStatusPublicationRejectsAConfigChangeDuringCommit(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires two PostgreSQL writers")
	}
	s, _ := open(t)
	seed, err := s.CommitConfig(ConfigChange{Action: "put"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", currentDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, `UPDATE sequences SET seq = seq + 1 WHERE bucket = $1`, string(bucketConfigChanges)); err != nil {
		t.Fatal(err)
	}
	const draft = "status/public"
	result := make(chan error, 1)
	go func() {
		_, err := s.CommitConfigWithMaintenance(ConfigChange{Action: "put"}, []ConfigWrite{{Key: draft, Value: []byte(`{}`)}}, seed.Version, 0)
		result <- err
	}()
	// Wait until publication passed its first version read and is contending for the sequence.
	ticks := time.NewTicker(10 * time.Millisecond)
	defer ticks.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND query LIKE 'INSERT INTO sequences%'
		)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("publication finished without obtaining the sequence: %v", err)
		case <-ctx.Done():
			t.Fatal("publication did not reach the sequence lock")
		case <-ticks.C:
		}
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("publication accepted an obsolete configuration: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("publication did not resume after the intervening commit")
	}
	entries, version, err := s.Config()
	if err != nil || version != seed.Version+1 || entries[draft] != nil {
		t.Fatalf("a rejected publication changed configuration: %v %d %v", entries, version, err)
	}
}

func TestConfigVersions(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if e, v, err := s.Config(); err != nil || len(e) != 0 || v != 0 {
		t.Fatalf("a new database has no configuration: %v %d %v", e, v, err)
	}
	if got := s.ConfigChanges(0, 10, nil); len(got) != 0 {
		t.Errorf("no changes yet: %v", got)
	}
	c, err := s.CommitConfig(ConfigChange{Actor: "a", Via: "api", Action: "put"}, []ConfigWrite{{Key: "location/x", Value: []byte(`{"id":"x"}`)}})
	if err != nil || c.Version != 1 || c.Time.IsZero() {
		t.Fatalf("commit: %+v %v", c, err)
	}
	// a write that expects what is no longer there changes nothing
	if _, err := s.CommitConfig(ConfigChange{Action: "put"}, []ConfigWrite{
		{Key: "location/y", Value: []byte(`{}`)},
		{Key: "location/x", Value: []byte(`{"id":"x","name":"n"}`)}, // expects it absent
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if e, v, err := s.Config(); err != nil || len(e) != 1 || v != 1 {
		t.Errorf("all or nothing: %v %d %v", e, v, err)
	}
	for i := range 4 {
		w := ConfigWrite{Key: "location/x", Was: []byte(`{"id":"x"}`)}
		if i%2 == 1 {
			w = ConfigWrite{Key: "location/x", Value: []byte(`{"id":"x"}`)}
		}
		if _, err := s.CommitConfig(ConfigChange{Action: "put"}, []ConfigWrite{w}); err != nil {
			t.Fatal(err)
		}
	}
	if _, v, err := s.Config(); err != nil || v != 5 {
		t.Errorf("version %d %v", v, err)
	}
	list := s.ConfigChanges(0, 2, nil)
	if len(list) != 2 || list[0].Version != 5 || list[1].Version != 4 {
		t.Errorf("newest first: %+v", list)
	}
	if page := s.ConfigChanges(4, 10, nil); len(page) != 3 || page[0].Version != 3 {
		t.Errorf("older than 4: %+v", page)
	}
	if page := s.ConfigChanges(99, 1, nil); len(page) != 1 || page[0].Version != 5 {
		t.Errorf("older than a version not made yet: %+v", page)
	}
	if page := s.ConfigChanges(1, 10, nil); len(page) != 0 {
		t.Errorf("nothing is older than the first: %+v", page)
	}
	if odd := s.ConfigChanges(0, 10, func(c ConfigChange) bool { return c.Version%2 == 1 }); len(odd) != 3 {
		t.Errorf("filter: %+v", odd)
	}
	if c, ok := s.ConfigChangeAt(1); !ok || c.Actor != "a" {
		t.Errorf("at 1: %+v", c)
	}

	keepConfigChanges = 3
	t.Cleanup(func() { keepConfigChanges = 10000 })
	if _, err := s.CommitConfig(ConfigChange{Action: "put"}, nil); err != nil {
		t.Fatal(err)
	}
	kept := s.ConfigChanges(0, 100, nil)
	if len(kept) != 3 || kept[0].Version != 6 || kept[2].Version != 4 {
		t.Errorf("the oldest changes beyond the limit are dropped: %+v", kept)
	}
	if _, v, err := s.Config(); err != nil || v != 6 {
		t.Errorf("versions go on: %d %v", v, err)
	}
}

func TestConfigReadError(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitConfig(ConfigChange{Action: "put"}, []ConfigWrite{{Key: "location/x", Value: []byte(`{"id":"x"}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	e, v, err := s.Config()
	if err == nil {
		t.Fatal("Config after the database closed must return the read error")
	}
	if len(e) != 0 || v != 0 {
		t.Fatalf("a failed read must not look like an empty catalogue: %d entries version %d", len(e), v)
	}
}

func TestConsoleTargetOwnershipOutlivesConfigHistory(t *testing.T) {
	keep := keepConfigChanges
	keepConfigChanges = 1
	t.Cleanup(func() { keepConfigChanges = keep })
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	const name = "all-http.example.monitor"
	first := []byte(`[{"targets":["https://example.com/ready"]}]`)
	if _, err := s.CommitConfig(ConfigChange{Items: []ConfigItem{{
		Kind: "targets", Key: name, After: first, ConsoleTracked: true, ConsoleAfter: true,
	}}}, []ConfigWrite{{Key: "targets/" + name, Value: first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitConfig(ConfigChange{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ConfigChangeAt(1); ok {
		t.Fatal("the target's creating event should have been pruned")
	}
	if _, owners, version, err := s.ConfigWithConsoleTargets(); err != nil || !owners[name] || version != 2 {
		t.Fatalf("ownership was lost when config history was pruned: %v %d %v", owners, version, err)
	}
	replacement := []byte(`[{"targets":["https://example.com/health"]}]`)
	change := ConfigChange{Items: []ConfigItem{{Kind: "targets", Key: name, Before: first, After: replacement,
		ConsoleTracked: true, ConsoleBefore: true}}}
	writes := []ConfigWrite{{Key: "targets/" + name, Was: first, Value: replacement}}
	if _, err := s.CommitConfigAtVersion(change, writes, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version could change ownership: %v", err)
	}
	if _, err := s.CommitConfigAtVersion(change, writes, 2); err != nil {
		t.Fatal(err)
	}
	if _, owners, version, err := s.ConfigWithConsoleTargets(); err != nil || owners[name] || version != 3 {
		t.Fatalf("external replacement kept console ownership: %v %d %v", owners, version, err)
	}
}
