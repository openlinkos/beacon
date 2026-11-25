package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIncidentEpisodeRecordsReadRestoredAndArchivedStateTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episodes.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	const group = "0123456789ab"
	const open, closed = "1111111111111111", "2222222222222222"
	g := Group{ID: group, Episode: open, Status: "firing", Severity: "warning", OpeningSeverity: "critical", StartsAt: now.Add(-time.Minute)}
	o := IncidentOutcome{Group: group, Severity: "critical", OpeningSeverity: "critical", Started: now.Add(-2 * time.Hour), Resolved: now.Add(-time.Hour)}
	o.ID = OutcomeID(o.Resolved, group, closed)
	if err := db.PutGroupWithOutcome(g, &o); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.IncidentEpisodeRecords(t.Context(), IncidentEpisodeFilter{Since: now.Add(-24 * time.Hour), Until: now,
		RetainedFrom: now.Add(-48 * time.Hour), Severity: "critical", Limit: 2})
	if err != nil || len(rows) != 2 || rows[0].Episode != open || rows[0].Current == nil ||
		rows[1].Episode != closed || rows[1].Archived == nil {
		t.Fatalf("restored starts and resolved archive disagree: %+v %v", rows, err)
	}
}
