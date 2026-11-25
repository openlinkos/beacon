package store

import (
	"errors"
	"testing"

	"github.com/openlinkos/beacon/internal/kv"
)

func TestMachineHistoryLegacyAndIdentity(t *testing.T) {
	s, _ := open(t)
	original := Machine{Name: "edge-1", Address: ConnectorAddress, User: "beacon", Note: "initial", ConnectorKey: "old"}
	if err := s.db.Update(func(tx kv.Tx) error { return writeMachine(tx, original) }); err != nil {
		t.Fatal(err)
	}
	h, err := s.MachineMetadataHistory(original.Name, 0, 10)
	if err != nil || h.Version != 0 || len(h.Revisions) != 0 {
		t.Fatalf("before first edit: %+v %v", h, err)
	}
	newUser, critical := "ops", true
	_, after, err := s.PatchMachineMetadata(original.Name, MachineMetadataUpdate{User: &newUser, Critical: &critical}, 0, "alice", "console")
	if err != nil || after.MetadataVersion <= 1 || after.Registration() != original.Registration() {
		t.Fatalf("first edit: %+v %v", after, err)
	}
	h, err = s.MachineMetadataHistory(original.Name, after.MetadataVersion, 1)
	if err != nil || len(h.Revisions) != 1 || h.Revisions[0].Metadata != original.metadata() || h.Revisions[0].Action != "baseline" {
		t.Fatalf("first page before edit: %+v %v", h, err)
	}
	baseline := h.Revisions[0].Version
	if _, err := s.UpdateMachineIdentity(original.Name, func(Machine) bool { return true }, func(m *Machine) {
		m.ConnectorKey, m.ConnectorFingerprint, m.HostKey = "new", "new-fp", "new-host"
	}); err != nil {
		t.Fatal(err)
	}
	_, restored, err := s.PatchMachineMetadata(original.Name, MachineMetadataUpdate{IfVersion: &after.MetadataVersion}, baseline, "alice", "console")
	if err != nil || restored.metadata() != original.metadata() || restored.ConnectorKey != "new" || restored.ConnectorFingerprint != "new-fp" || restored.HostKey != "new-host" || restored.Address != ConnectorAddress {
		t.Fatalf("restore must only copy metadata: %+v %v", restored, err)
	}
	if restored.MetadataVersion <= after.MetadataVersion {
		t.Fatal("restore did not make a fresh revision")
	}
	if _, _, err := s.PatchMachineMetadata(original.Name, MachineMetadataUpdate{IfVersion: &after.MetadataVersion}, baseline, "alice", "console"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale restore: %v", err)
	}
	if err := s.DeleteMachine(original.Name); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PatchMachineMetadata(original.Name, MachineMetadataUpdate{IfVersion: &restored.MetadataVersion}, baseline, "alice", "console"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore on a deleted registration: %v", err)
	}
	fresh, err := s.RegisterMachine(original, nil, "api")
	if err != nil || fresh.MetadataVersion <= restored.MetadataVersion || fresh.Registration() == original.Registration() {
		t.Fatalf("re-enrolment: %+v %v", fresh, err)
	}
	if _, _, err := s.PatchMachineMetadata(original.Name, MachineMetadataUpdate{IfVersion: &fresh.MetadataVersion}, baseline, "alice", "console"); !errors.Is(err, ErrMachineVersion) {
		t.Fatalf("restore across enrolments: %v", err)
	}
	h, err = s.MachineMetadataHistory(original.Name, 0, 10)
	if err != nil || len(h.Revisions) != 1 || h.Revisions[0].Version != fresh.MetadataVersion || h.Revisions[0].Via != "api" {
		t.Fatalf("history of the replacement: %+v %v", h, err)
	}
}

func TestMachineHistoryConcurrentEdit(t *testing.T) {
	s, _ := open(t)
	m, err := s.RegisterMachine(Machine{Name: "edge-1", User: "beacon"}, nil, "api")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, note := range []string{"first", "second"} {
		go func() {
			_, _, e := s.PatchMachineMetadata(m.Name, MachineMetadataUpdate{Note: &note, IfVersion: &m.MetadataVersion}, 0, "alice", "api")
			results <- e
		}()
	}
	first, second := <-results, <-results
	if first != nil && second == nil {
		first, second = second, first
	}
	if first != nil || !errors.Is(second, ErrConflict) {
		t.Fatalf("want one edit and one conflict, got %v and %v", first, second)
	}
	h, err := s.MachineMetadataHistory(m.Name, 0, 10)
	if err != nil || len(h.Revisions) != 2 || h.Version != h.Revisions[0].Version {
		t.Fatalf("a stale edit must not add a revision: %+v %v", h, err)
	}
}
