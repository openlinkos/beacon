package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/exercise"
)

func TestDrillRunAndObservationsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UTC()
	if err := s.SetTime("drill", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if legacy := s.DrillRun(); legacy.ID != "" || !legacy.Active(now) {
		t.Fatalf("older drill did not retain its ordinary response: %+v", legacy)
	}
	run, wasActive, err := s.SetDrill(now, now.Add(time.Minute), "cli:fixture", "1m")
	if err != nil || !wasActive || run.ID == "" {
		t.Fatalf("arm after legacy: %+v %v %v", run, wasActive, err)
	}
	attested := exercise.Observation{
		Run: run.ID, Location: "edge-1", Target: exercise.Target{URL: "https://status.example.com/drill/healthz", Module: "http_2xx"},
		Sampled: now.Add(time.Second), Served: now.Add(time.Second), Recorded: now.Add(2 * time.Second),
	}
	if err := s.RecordDrillObservation(attested); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetDrill(now.Add(5*time.Second), time.Time{}, "cli:fixture", "off"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	stopped, evidence, err := s.DrillEvidence()
	if err != nil || stopped.ID != run.ID || stopped.Active(now.Add(6*time.Second)) || len(evidence) != 1 || evidence[0].Run != run.ID {
		t.Fatalf("restart lost exact source proof: %+v %+v %v", stopped, evidence, err)
	}
	next, _, err := s.SetDrill(now.Add(6*time.Second), now.Add(time.Minute), "cli:fixture", "1m")
	if err != nil || next.ID == run.ID {
		t.Fatalf("rearm reused the old identity: %+v %v", next, err)
	}
	if err := s.RecordDrillObservation(attested); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the replay was accepted for a different run: %v", err)
	}
	if current, rows, err := s.DrillEvidence(); err != nil || current.ID != next.ID || len(rows) != 0 {
		t.Fatalf("source evidence spilled into the next run: %+v %+v %v", current, rows, err)
	}
}
