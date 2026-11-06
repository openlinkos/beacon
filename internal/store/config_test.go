package store

import (
	"errors"
	"path/filepath"
	"testing"
)

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
