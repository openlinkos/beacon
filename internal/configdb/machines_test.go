package configdb

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
)

// A heartbeat may name the machine its job runs on. An import takes a machine that is not enrolled yet
// and warns; a put refuses one, unless the heartbeat named it already; a revert takes it, as an import.
func TestHeartbeatMachine(t *testing.T) {
	e := newEnv(t)
	f := testFiles()
	f["catalog/other.yaml"] = strings.Replace(f["catalog/other.yaml"], "description: Nightly backup of the NAS}",
		"description: Nightly backup of the NAS, machine: nas-1}", 1)
	ch, err := e.s.Import(ctx, Actor{Name: "linkos-beacon", Via: "start"}, f, "", false)
	if err != nil {
		t.Fatalf("an import takes a machine not enrolled yet: %v", err)
	}
	if len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], `heartbeat "nas-backup" names machine "nas-1", which is not enrolled`) {
		t.Errorf("the import warns: %q", ch.Warnings)
	}
	if w := e.s.Warnings(); len(w) != 1 || w[0] != ch.Warnings[0] {
		t.Errorf("check-config warns too: %q", w)
	}
	if h, ok := e.s.Catalog().Heartbeat("nas-backup"); !ok || h.Machine != "nas-1" {
		t.Errorf("in force: %+v", h)
	}
	if stored, ok := e.st.ConfigChangeAt(ch.Version); !ok || stored.Warnings != nil {
		t.Errorf("warnings are not stored: %+v", stored)
	}

	// the export writes it, and importing the export changes nothing
	out, _, err := e.s.Export()
	if err != nil || !strings.Contains(out["catalog/heartbeats.yaml"], "machine: nas-1\n") {
		t.Fatalf("export: %v\n%s", err, out["catalog/heartbeats.yaml"])
	}
	if again, err := e.s.Import(ctx, api, out, "", false); err != nil || !again.Unchanged || len(again.Warnings) != 1 {
		t.Errorf("round trip: %+v %v", again, err)
	}

	// a put of a machine that is not enrolled is refused, and says what to do
	v := e.s.Version()
	_, err = e.s.Put(ctx, api, "heartbeats", "db-dump", json.RawMessage(`{"max_age": "1h", "severity": "critical", "machine": "db-1"}`), "")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `machine "db-1" is not enrolled on the SSH channel; enrol it first`) {
		t.Errorf("an unknown machine: %v", err)
	}
	_, err = e.s.Put(ctx, api, "heartbeats", "db-dump", json.RawMessage(`{"max_age": "1h", "severity": "critical", "machine": "DB_1"}`), "")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "is not a machine's name") {
		t.Errorf("not a machine's name: %v", err)
	}
	if e.s.Version() != v {
		t.Fatal("a refused put makes no version")
	}
	// the heartbeat that named its machine already can still be changed
	ch, err = e.s.Put(ctx, api, "heartbeats", "nas-backup", json.RawMessage(`{"max_age": "30h", "severity": "warning", "machine": "nas-1"}`), "")
	if err != nil || len(ch.Warnings) != 1 {
		t.Errorf("the same machine as before: %+v %v", ch, err)
	}
	// once the machine is enrolled, the put goes through
	if err := e.st.PutMachine(store.Machine{Name: "db-1", Address: "db-1.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	ch, err = e.s.Put(ctx, api, "heartbeats", "db-dump", json.RawMessage(`{"max_age": "1h", "severity": "critical", "machine": "db-1"}`), "")
	if err != nil || ch.Items[0].Op != "added" || !strings.Contains(string(ch.Items[0].After), `"machine":"db-1"`) {
		t.Fatalf("an enrolled machine: %+v %v", ch, err)
	}
	put := ch.Version
	if err := e.st.PutMachine(store.Machine{Name: "nas-1", Address: "nas-1.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	if w := e.s.Warnings(); len(w) != 0 {
		t.Errorf("every machine enrolled: %q", w)
	}

	// a revert takes a machine that has gone meanwhile, and warns
	del, err := e.s.Delete(ctx, api, "heartbeats", "db-dump", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteMachine("db-1"); err != nil {
		t.Fatal(err)
	}
	ch, err = e.s.Revert(ctx, api, del.Version, "")
	if err != nil || len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], `"db-1"`) {
		t.Errorf("revert: %+v %v", ch, err)
	}
	if _, err := e.s.Revert(ctx, api, put, ""); err != nil {
		t.Errorf("reverting the put removes the heartbeat again: %v", err)
	}
}
