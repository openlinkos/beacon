package store

import (
	"database/sql"
	"testing"
)

// TestAuditFailuresAreReported checks that an audit entry that cannot be written is reported, not
// dropped in silence.
func TestAuditFailuresAreReported(t *testing.T) {
	s, path := open(t)
	if currentDSN != "" {
		t.Skip("the failing write is made with a SQLite trigger")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}
	var got []AuditEntry
	s.OnAuditFailure(func(e AuditEntry, err error) {
		if err == nil {
			t.Error("a failure without its error")
		}
		got = append(got, e)
	})
	if err := s.Audit(AuditEntry{Actor: "alice", Via: "api", Action: "revoke_token", Target: "ci"}); err == nil {
		t.Fatal("the write did not fail")
	}
	if len(got) != 1 || got[0].Action != "revoke_token" || got[0].Target != "ci" {
		t.Errorf("reported: %+v", got)
	}
	s.OnAuditFailure(nil) // the default logs it
	if err := s.Audit(AuditEntry{Actor: "alice", Via: "api", Action: "revoke_token", Target: "ci"}); err == nil || len(got) != 1 {
		t.Errorf("after the hook was removed: %v, %d reported", err, len(got))
	}
}
