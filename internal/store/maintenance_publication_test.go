package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

func TestMaintenanceRevisionSerialisesWithPublication(t *testing.T) {
	editor, path := open(t)
	writer := editor
	if currentDSN != "" {
		// PostgreSQL has its own write pool for each connected process.
		var err error
		writer, err = reopen(t, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = writer.Close() })
	}
	snapshot, err := editor.MaintenanceSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	written := make(chan struct{})
	release := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- writer.db.Update(func(tx kv.Tx) error {
			if _, err := tx.NextSequence(bucketMaintenanceRevision); err != nil {
				return err
			}
			if err := tx.Put(bucketMaintenance, []byte("new"), json.RawMessage(`{"id":"new"}`)); err != nil {
				return err
			}
			close(written)
			<-release
			return nil
		})
	}()
	select {
	case <-written:
	case err := <-writerDone:
		close(release)
		t.Fatalf("staging the concurrent window failed: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the concurrent window did not obtain the write lock")
	}
	result := make(chan error, 1)
	go func() {
		_, err := editor.CommitConfigWithMaintenance(ConfigChange{Action: "put"}, nil, 0, snapshot.Revision)
		result <- err
	}()
	select {
	case err := <-result:
		close(release)
		<-writerDone
		t.Fatalf("publication completed while the maintenance writer still held the revision lock: %v", err)
	case <-time.After(50 * time.Millisecond):
		close(release)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrMaintenanceChanged) {
			t.Fatalf("the committed window did not invalidate publication: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the publication did not resume after the maintenance commit")
	}
	if got, err := editor.ConfigVersion(); err != nil || got != 0 {
		t.Fatalf("configuration changed after a rejected publication: version %d, error %v", got, err)
	}
}

func TestIncidentChangesInvalidateTheReviewedStatusPublication(t *testing.T) {
	s, _ := open(t)
	now := time.Date(2026, time.November, 1, 1, 0, 0, 0, time.UTC)
	incident := Incident{ID: "a1", Status: "firing", StartsAt: now}
	if _, err := s.PutIncidents([]Incident{incident}, now); err != nil {
		t.Fatal(err)
	}
	read := func() MaintenanceSnapshot {
		t.Helper()
		snapshot, _, err := s.StatusPublicationSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	previous := read()
	if changed, err := s.PutIncidents([]Incident{incident}, now); err != nil || changed || read().Revision != previous.Revision {
		t.Fatalf("repeating an unchanged incident advanced the reviewed content: %v, %v", changed, err)
	}
	incident.Notes = []IncidentNote{{At: now, Text: "The service is recovering."}}
	if _, err := s.PutIncidents([]Incident{incident}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitConfigWithMaintenance(ConfigChange{Action: "put"}, nil, 0, previous.Revision); !errors.Is(err, ErrMaintenanceChanged) {
		t.Fatalf("a stale incident snapshot permitted publication: %v", err)
	}
	if _, err := s.CommitConfigWithMaintenance(ConfigChange{Action: "put"}, nil, 0, read().Revision); err != nil {
		t.Fatalf("the current incident snapshot could not be published: %v", err)
	}
}
