package bridge

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Beacon's own changes reach the evidence: a version it starts on, and a drill with its end.
func TestOwnChanges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.b.RecordStart(ctx, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.b.RecordStart(ctx, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	if err := e.b.RecordStart(ctx, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.b.Changes(ChangeQuery{Source: "beacon"})
	if len(got) != 2 {
		t.Fatalf("two starts on two versions, a restart is none: %+v", got)
	}
	if c := got[0]; c.Kind != "deploy" || c.Revision != "v1.1.0" || c.Summary != "Beacon v1.1.0 started, after v1.0.0." || c.PostedBy != "beacon" {
		t.Errorf("the upgrade: %+v", c)
	}
	if c := got[1]; c.Summary != "Beacon v1.0.0 started." {
		t.Errorf("the first start: %+v", c)
	}

	e.now = e.now.Add(time.Minute)
	until := e.now.Add(15 * time.Minute)
	if err := e.b.RecordDrill(ctx, until, "cli:root"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(5 * time.Minute)
	if err := e.b.RecordDrill(ctx, time.Time{}, "cli:root"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.b.Changes(ChangeQuery{Service: "drill"})
	if len(got) != 2 {
		t.Fatalf("a drill's start and end: %+v", got)
	}
	if c := got[1]; c.Kind != "drill" || c.Source != "beacon" || c.PostedBy != "cli:root" ||
		!strings.Contains(c.Summary, "until 2026-10-30T02:17:00Z, when it ends by itself") {
		t.Errorf("the start says when the drill ends: %+v", c)
	}
	if c := got[0]; !strings.HasPrefix(c.Summary, "Drill ended") {
		t.Errorf("the end: %+v", c)
	}
}
