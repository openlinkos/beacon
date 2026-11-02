package configdb

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestIfVersion: a change that names the version it is based on is refused with ErrConflict, naming
// the version in force, once a later one exists; with the version in force, or none, it goes through.
func TestIfVersion(t *testing.T) {
	e := imported(t)
	v := e.s.Version()
	heartbeat := json.RawMessage(`{"max_age": "48h", "severity": "info"}`)
	ch, err := e.s.PutIf(ctx, api, "heartbeats", "weekly", heartbeat, "", v)
	if err != nil || ch.Version != v+1 {
		t.Fatalf("based on the version in force: %+v %v", ch, err)
	}
	_, err = e.s.PutIf(ctx, api, "heartbeats", "weekly", json.RawMessage(`{"max_age": "72h", "severity": "info"}`), "", v)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "version 2 is in force, not 1") {
		t.Fatalf("based on a stale version: %v", err)
	}
	if _, err := e.s.DeleteIf(ctx, api, "heartbeats", "weekly", "", v); !errors.Is(err, ErrConflict) {
		t.Errorf("a stale delete: %v", err)
	}
	if _, err := e.s.DeleteIf(ctx, api, "heartbeats", "nothing", "", v); !errors.Is(err, ErrConflict) {
		t.Errorf("the conflict is seen before the item is looked for: %v", err)
	}
	if _, err := e.s.DeleteIf(ctx, api, "heartbeats", "nothing", "", v+1); !errors.Is(err, ErrNotFound) {
		t.Errorf("not found with the version in force: %v", err)
	}
	if _, err := e.s.PutIf(ctx, api, "heartbeats", "weekly", heartbeat, "", -1); !errors.Is(err, ErrBadInput) {
		t.Errorf("a negative version: %v", err)
	}
	if e.s.Version() != v+1 {
		t.Error("a refused change changed something")
	}
	if ch, err := e.s.DeleteIf(ctx, api, "heartbeats", "weekly", "", v+1); err != nil || ch.Version != v+2 {
		t.Fatalf("delete with the version in force: %+v %v", ch, err)
	}
	if ch, err := e.s.Put(ctx, api, "heartbeats", "weekly", heartbeat, ""); err != nil || ch.Version != v+3 {
		t.Fatalf("without a condition: %+v %v", ch, err)
	}
}
