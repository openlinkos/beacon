package store

import (
	"fmt"
	"testing"
	"time"
)

func TestChanges(t *testing.T) {
	s, _ := open(t)
	if got := s.Changes(ChangeFilter{}); len(got) != 0 {
		t.Fatalf("no bucket yet: %v", got)
	}
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for i, svc := range []string{"hub", "git", "hub", "hub"} {
		c, err := s.PutChange(Change{Time: t0.Add(time.Duration(i) * time.Minute), Source: "ci", Kind: "deploy", Service: svc, Summary: "v" + string(rune('1'+i))}, t0.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("20261102T10%02d00.000000000-", i); c.ID[:len(want)] != want || len(c.ID) != len(want)+8 {
			t.Fatalf("ids begin with the time: %s", c.ID)
		}
	}
	all := s.Changes(ChangeFilter{})
	if len(all) != 4 || all[0].Summary != "v4" || all[3].Summary != "v1" {
		t.Fatalf("newest first: %+v", all)
	}
	got := s.Changes(ChangeFilter{From: t0.Add(time.Minute), To: t0.Add(2 * time.Minute)})
	if len(got) != 2 || got[0].Summary != "v3" || got[1].Summary != "v2" {
		t.Fatalf("between, both ends included: %+v", got)
	}
	if got := s.Changes(ChangeFilter{Service: "hub", Limit: 2}); len(got) != 2 || got[0].Summary != "v4" || got[1].Summary != "v3" {
		t.Fatalf("by service, limited: %+v", got)
	}
	if got := s.Changes(ChangeFilter{To: t0.Add(-time.Second)}); len(got) != 0 {
		t.Fatalf("before them all: %+v", got)
	}
	// a new change forgets those before the cut
	if _, err := s.PutChange(Change{Time: t0.Add(time.Hour), Source: "ci", Kind: "deploy", Summary: "v5"}, t0.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := s.Changes(ChangeFilter{}); len(got) != 3 || got[2].Summary != "v3" {
		t.Fatalf("old changes forgotten: %+v", got)
	}
}

func TestSessionsBetween(t *testing.T) {
	s, _ := open(t)
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	put := func(id string, start, end time.Time, machine string) {
		if err := s.PutSession(SessionRecord{ID: id, Machine: machine, Started: start, Ended: end}); err != nil {
			t.Fatal(err)
		}
	}
	put("20261101-080000-aaaaaa-1", t0.Add(-26*time.Hour), t0.Add(-25*time.Hour), "edge-1") // long before
	put("20261102-092000-bbbbbb-1", t0.Add(-40*time.Minute), t0.Add(-20*time.Minute), "edge-1")
	put("20261102-093000-cccccc-1", t0.Add(-30*time.Minute), time.Time{}, "edge-1") // still open
	put("20261102-100500-dddddd-1", t0.Add(5*time.Minute), t0.Add(6*time.Minute), "edge-2")
	put("20261102-120000-eeeeee-1", t0.Add(2*time.Hour), t0.Add(3*time.Hour), "edge-1") // after
	got := s.SessionsBetween(t0.Add(-10*time.Minute), t0.Add(time.Hour), 10, nil)
	if len(got) != 2 || got[0].ID != "20261102-093000-cccccc-1" || got[1].ID != "20261102-100500-dddddd-1" {
		t.Fatalf("open within the window: %+v", got)
	}
	got = s.SessionsBetween(t0.Add(-time.Hour), t0.Add(time.Hour), 10, func(r SessionRecord) bool { return r.Machine == "edge-1" })
	if len(got) != 2 || got[0].ID != "20261102-092000-bbbbbb-1" {
		t.Fatalf("kept by machine: %+v", got)
	}
}
