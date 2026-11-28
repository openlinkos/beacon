package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// TestProbeQueriesReadProbesOnly: the evidence, the card's locations and a heartbeat's last success
// read only what the gateway labelled as written by a probe location (or, for heartbeats, by a
// heartbeat, probe or host token), so that a host's or a heartbeat's samples cannot pass for a probe's.
func TestProbeQueriesReadProbesOnly(t *testing.T) {
	e := newEnv(t)
	v := &heartbeatVM{}
	e.b.d.VM = v
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	e.b.Probes(context.Background(), []string{"git"}, Window{From: now.Add(-time.Hour), To: now}, 10)
	e.b.locations(context.Background(), &store.Group{Labels: map[string]string{"service": "git"}})
	e.b.lastSuccess(context.Background(), []string{"nas-backup"}, now)
	if len(v.queries) < 5 {
		t.Fatalf("queries: %q", v.queries)
	}
	for _, q := range v.queries {
		want := `job="probe"`
		if strings.Contains(q, "beacon_heartbeat_last_success_timestamp_seconds") {
			want = `job=~"heartbeat|probe|host"`
		}
		if strings.Count(q, "{") != strings.Count(q, want) {
			t.Errorf("a selector without %s: %s", want, q)
		}
	}
}
