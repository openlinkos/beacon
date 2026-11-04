package store

import (
	"strings"
	"testing"
	"time"
)

func TestConsoleSessions(t *testing.T) {
	s, _ := open(t)
	defer func() { _ = s.Close() }()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	_, tok, err := s.CreateToken("alice", []string{"status:read"}, 30*24*time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}

	id, cs, err := s.CreateConsoleSession(tok, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) < 40 || cs.Token != "alice" || !cs.TokenCreated.Equal(tok.Created) || !cs.Expires.Equal(now.Add(12*time.Hour)) || len(cs.CSRF) < 40 {
		t.Fatalf("session %q %+v", id, cs)
	}
	if strings.Contains(cs.Hash, id) || cs.Hash == id {
		t.Error("the session's id is stored as it is")
	}
	got, err := s.ConsoleSessionByID(id)
	if err != nil || got.CSRF != cs.CSRF {
		t.Fatalf("lookup: %v %+v", err, got)
	}
	if _, err := s.ConsoleSessionByID(id + "x"); err == nil {
		t.Error("a wrong id finds a session")
	}
	if _, err := s.ConsoleSessionByID(""); err == nil {
		t.Error("an empty id finds a session")
	}

	// a session ends after 12 hours
	s.now = func() time.Time { return now.Add(12 * time.Hour) }
	if _, err := s.ConsoleSessionByID(id); err == nil {
		t.Error("the session outlives its 12 hours")
	}
	s.now = func() time.Time { return now }

	// a token that expires sooner ends the session sooner
	_, short, err := s.CreateToken("bob", []string{"status:read"}, 2*time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, bcs, err := s.CreateConsoleSession(short, 12*time.Hour)
	if err != nil || !bcs.Expires.Equal(short.Expires) {
		t.Fatalf("short token: %v %v, want %v", err, bcs.Expires, short.Expires)
	}

	// signing out ends one session; revoking ends every session of the token
	id2, _, _ := s.CreateConsoleSession(tok, 12*time.Hour)
	if err := s.DeleteConsoleSession(id2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsoleSessionByID(id2); err == nil {
		t.Error("a deleted session lives on")
	}
	id3, _, _ := s.CreateConsoleSession(tok, 12*time.Hour)
	if n := s.DeleteConsoleSessions("alice"); n != 2 {
		t.Errorf("deleted %d of alice's sessions, want 2", n)
	}
	for _, x := range []string{id, id3} {
		if _, err := s.ConsoleSessionByID(x); err == nil {
			t.Error("a session of a revoked token lives on")
		}
	}

	// sessions that ended are forgotten when the next one starts
	s.now = func() time.Time { return now.Add(3 * time.Hour) }
	if _, _, err := s.CreateConsoleSession(tok, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	if n := s.DeleteConsoleSessions("bob"); n != 0 {
		t.Errorf("bob's ended session was kept: %d", n)
	}
}

func TestEachSession(t *testing.T) {
	s, _ := open(t)
	defer func() { _ = s.Close() }()
	for _, id := range []string{"20261101-000000-aaaaaa", "20261102-000000-bbbbbb", "20261103-000000-cccccc"} {
		if err := s.PutSession(SessionRecord{ID: id, Machine: "edge-1"}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	s.EachSession(func(r SessionRecord) bool {
		seen = append(seen, r.ID[:8])
		return len(seen) < 2
	})
	if strings.Join(seen, " ") != "20261103 20261102" {
		t.Errorf("walked %v", seen)
	}
}
