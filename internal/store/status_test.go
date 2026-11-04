package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIncidentsAndMaintenance(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if len(s.Incidents()) != 0 || len(s.MaintenanceWindows()) != 0 {
		t.Fatal("an older database has neither")
	}
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	old := Incident{ID: "old", AlertName: "ServiceDown", Status: "resolved", Services: []string{"a"}, StartsAt: now.Add(-100 * 24 * time.Hour),
		ResolvedAt: now.Add(-99 * 24 * time.Hour)}
	live := Incident{ID: "live", AlertName: "ServiceDown", Status: "firing", Services: []string{"a"}, Firing: []string{"a"}, StartsAt: now.Add(-time.Hour),
		Notes: []IncidentNote{{At: now, Text: "public: on it", Agent: true}}}
	if wrote, err := s.PutIncidents([]Incident{old, live}, now.Add(-KeepStatus)); err != nil || !wrote {
		t.Fatalf("put: %v %v", wrote, err)
	}
	got := s.Incidents()
	if len(got) != 1 || got[0].ID != "live" || !got[0].Notes[0].Agent {
		t.Errorf("an incident that ended before the history began is forgotten: %+v", got)
	}
	if wrote, _ := s.PutIncidents([]Incident{live}, now.Add(-KeepStatus)); wrote {
		t.Error("an unchanged incident is not written again")
	}

	m := Maintenance{ID: "m-1", Title: "Upgrade", Services: []string{"a"}, StartsAt: now, EndsAt: now.Add(time.Hour)}
	gone := Maintenance{ID: "m-0", Title: "Long ago", Services: []string{"a"}, StartsAt: now.Add(-200 * 24 * time.Hour), EndsAt: now.Add(-199 * 24 * time.Hour)}
	if err := s.PutMaintenance(gone, now.Add(-300*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMaintenance(m, now.Add(-KeepStatus)); err != nil {
		t.Fatal(err)
	}
	if ws := s.MaintenanceWindows(); len(ws) != 1 || ws[0].ID != "m-1" {
		t.Errorf("windows: %+v", ws)
	}
	m.Ended = now.Add(10 * time.Minute)
	if m.End() != m.Ended {
		t.Error("a window ended early ends then")
	}
	if _, ok := s.MaintenanceWindow("m-1"); !ok {
		t.Error("by id")
	}
}
