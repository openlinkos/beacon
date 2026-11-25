package store

import (
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/openlinkos/beacon/internal/kv"
)

func TestPageLinkRevocationPersistsAndAudits(t *testing.T) {
	s, path := open(t)
	who := AuditEntry{Actor: "alice", Via: "console"}
	read := func(st *Store, kind, id string, want uint64) {
		t.Helper()
		got, err := st.PageLinkGeneration(kind, id)
		if err != nil || got != want {
			t.Fatalf("generation for %s/%s: %d, %v; want %d", kind, id, got, err, want)
		}
	}
	read(s, "alert", "same-id", 0)
	read(s, "session", "same-id", 0)
	if _, err := s.RevokePageLinks("alert", "../other", who); !errors.Is(err, ErrPageLinkResource) {
		t.Fatalf("invalid ID: %v", err)
	}
	if _, err := s.RevokePageLinks("wrong", "same-id", who); !errors.Is(err, ErrPageLinkResource) {
		t.Fatalf("invalid kind: %v", err)
	}
	if got, err := s.RevokePageLinks("alert", "same-id", who); err != nil || got != 1 {
		t.Fatalf("first revocation: %d, %v", got, err)
	}
	if got, err := s.RevokePageLinks("alert", "same-id", who); err != nil || got != 2 {
		t.Fatalf("second revocation: %d, %v", got, err)
	}
	read(s, "session", "same-id", 0)
	read(s, "alert", "next-id", 0)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := reopen(t, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	read(restarted, "alert", "same-id", 2)
	read(restarted, "session", "same-id", 0)
	audit := restarted.AuditLog(10)
	for _, detail := range []string{"generation 1", "generation 2"} {
		if !slices.ContainsFunc(audit, func(e AuditEntry) bool {
			return e.Action == "revoke_page_links" && e.Actor == "alice" && e.Via == "console" &&
				e.Target == "alert/same-id" && e.Detail == detail
		}) {
			t.Fatalf("revocation audit missing: %s", detail)
		}
	}
	if _, err := restarted.VerifyAudit(); err != nil {
		t.Fatalf("revocations broke audit verification: %v", err)
	}
}

func TestPageLinkRevocationRollsBackWhenAuditFails(t *testing.T) {
	s, _ := open(t)
	if err := s.db.UpdateBoth(func(_ kv.Tx, raw *sql.Tx) error {
		_, err := raw.Exec("DROP TABLE audit_events")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RevokePageLinks("session", "fixture", AuditEntry{Actor: "alice", Via: "console"}); err == nil || got != 0 {
		t.Fatalf("failed audit left a reported revocation: %d, %v", got, err)
	}
	if got, err := s.PageLinkGeneration("session", "fixture"); err != nil || got != 0 {
		t.Fatalf("failed audit left the generation advanced: %d, %v", got, err)
	}
}

func TestPageLinkGenerationFailsOnMalformedRecord(t *testing.T) {
	s, _ := open(t)
	if err := s.db.Update(func(tx kv.Tx) error {
		return tx.Put(bucketPageLinkGenerations, []byte("alert/fixture"), []byte("invalid"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PageLinkGeneration("alert", "fixture"); err == nil {
		t.Fatal("an invalid generation could be treated as the legacy generation")
	}
}

func TestPageLinkRevocationConcurrentWriters(t *testing.T) {
	s, path := open(t)
	second := s
	if currentDSN != "" {
		var err error
		second, err = reopen(t, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = second.Close() })
	}
	type result struct {
		generation uint64
		err        error
	}
	results := make(chan result, 2)
	for _, writer := range []*Store{s, second} {
		go func() {
			generation, err := writer.RevokePageLinks("alert", "shared", AuditEntry{Actor: "operator", Via: "console"})
			results <- result{generation, err}
		}()
	}
	var got []uint64
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		got = append(got, r.generation)
	}
	slices.Sort(got)
	if !slices.Equal(got, []uint64{1, 2}) {
		t.Fatalf("concurrent revocations did not each advance the generation: %v", got)
	}
	if generation, err := s.PageLinkGeneration("alert", "shared"); err != nil || generation != 2 {
		t.Fatalf("the persisted generation lost a revocation: %d, %v", generation, err)
	}
}
