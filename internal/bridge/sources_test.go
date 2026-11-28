package bridge

import (
	"context"
	"slices"
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

// TestChangeSendersTextIsUntrusted: what a change's sender says of it (its source, kind and link as well as
// its summary) is listed as text from outside Beacon in the evidence and the timeline.
func TestChangeSendersTextIsUntrusted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	id := GroupID(hook("firing", "critical").GroupKey)
	ev, err := e.b.Evidence(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	tl, err := e.b.Timeline(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"changes[].source", "changes[].kind", "changes[].url", "changes[].summary"} {
		if !slices.Contains(ev.Untrusted.Fields, want) {
			t.Errorf("the evidence does not list %s as untrusted", want)
		}
	}
	for _, want := range []string{"events[].by", "events[].type", "events[].url", "events[].text"} {
		if !slices.Contains(tl.Untrusted.Fields, want) {
			t.Errorf("the timeline does not list %s as untrusted", want)
		}
	}
}
