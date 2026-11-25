package configdb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

func TestStatusPublicationChecksConcurrentMaintenanceSources(t *testing.T) {
	e := newEnv(t)
	editor, scheduler := e.st, e.st
	cfg := e.s
	ctx := context.Background()
	who := Actor{Name: "owner", Via: "console"}
	definition := json.RawMessage(`{"title":"Public","components":[{"name":"api","title":"API","services":["api"]}]}`)
	changed := json.RawMessage(`{"title":"Public","components":[{"name":"api","title":"API","services":["api","more"]}]}`)
	if _, err := cfg.Put(ctx, who, KindStatus, "public", definition, ""); err != nil {
		t.Fatal(err)
	}
	baseline := cfg.Version()
	now := time.Now()
	for _, source := range []struct {
		name string
		save func() error
	}{
		{"window", func() error {
			return scheduler.PutMaintenance(store.Maintenance{ID: "scheduled", StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour)},
				now.Add(-store.KeepStatus))
		}},
		{"silence", func() error {
			return scheduler.PutSilence(store.SilenceRecord{ID: "legacy", StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour),
				Comment: "maintenance: another operator's window"})
		}},
	} {
		t.Run(source.name, func(t *testing.T) {
			// The editor has recomputed the public effect just before confirmation. Another operation
			// commits a maintenance change before the editor can begin saving the page.
			reviewed, err := editor.MaintenanceSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := source.save(); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.PublishStatusPageIf(ctx, who, "public", changed, "", baseline, reviewed.Revision); !errors.Is(err, store.ErrMaintenanceChanged) {
				t.Fatalf("a concurrent %s did not invalidate confirmation: %v", source.name, err)
			}
			if version, err := editor.ConfigVersion(); err != nil || version != baseline {
				t.Fatalf("stale publication changed configuration: version %d, error %v", version, err)
			}
		})
	}
	fresh, err := editor.MaintenanceSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.PublishStatusPageIf(ctx, who, "public", changed, "", baseline, fresh.Revision); err != nil || cfg.Version() != baseline+1 {
		t.Fatalf("publication after fresh review failed: %v", err)
	}
}
