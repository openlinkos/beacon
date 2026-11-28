package router

import (
	"fmt"
	"testing"
	"time"
)

// TestSendersAreCapped: one alerts:send token holds at most SenderGroups groups and SenderAlerts firing
// alerts at once; new alerts beyond are dropped and counted, what it holds still changes and resolves,
// and the rule engine is never capped.
func TestSendersAreCapped(t *testing.T) {
	capped := map[string]int{}
	cfg := base
	cfg.Capped = func(sender, reason string) { capped[sender+"/"+reason]++ }
	e := newEnv(t, cfg)

	// 5000 distinct services in one post: SenderGroups groups, no more
	var many []map[string]string
	for i := range 5000 {
		many = append(many, down(fmt.Sprintf("svc-%d", i)))
	}
	e.post("flux", nil, many...)
	e.tick(31 * time.Second)
	if hooks := e.s.take(); len(hooks) != SenderGroups {
		t.Fatalf("%d groups notified, want %d", len(hooks), SenderGroups)
	}
	if capped["flux/groups"] != 5000-SenderGroups {
		t.Fatalf("capped %v", capped)
	}

	// many alerts in the groups it holds: SenderAlerts firing at once
	var same []map[string]string
	for i := range 2 * SenderAlerts {
		a := down("svc-0")
		a["instance"] = fmt.Sprintf("i-%d", i)
		same = append(same, a)
	}
	e.post("flux", nil, same...)
	if n := e.r.Firing(); n != SenderAlerts {
		t.Fatalf("%d firing, want %d", n, SenderAlerts)
	}
	if capped["flux/alerts"] == 0 {
		t.Fatalf("capped %v", capped)
	}

	// what it holds still resolves, and room comes back
	for _, l := range many[:SenderGroups] {
		l[SenderLabel] = ""
		_ = e.r.Ingest("flux", []Postable{{Labels: l, StartsAt: e.now.Add(-time.Hour), EndsAt: e.now.Add(-time.Second)}})
	}
	for _, l := range same {
		_ = e.r.Ingest("flux", []Postable{{Labels: l, StartsAt: e.now.Add(-time.Hour), EndsAt: e.now.Add(-time.Second)}})
	}
	e.tick(31 * time.Second)
	if n := e.r.Firing(); n != 0 {
		t.Fatalf("%d firing after resolving", n)
	}
	e.s.take()
	e.post("flux", nil, down("late"))
	e.tick(31 * time.Second)
	if hooks := e.s.take(); len(hooks) != 1 || hooks[0].CommonLabels["service"] != "late" {
		t.Fatalf("after resolving: %+v", hooks)
	}

	// another sender and the rule engine are not held to flux's count, nor is the rule engine to any
	e.post("ci", nil, down("ci-1"))
	e.fire(many...)
	e.tick(31 * time.Second)
	if hooks := e.s.take(); len(hooks) != 5001 {
		t.Fatalf("%d groups notified, want 5001", len(hooks))
	}
	if capped["ci/groups"] != 0 || capped["ci/alerts"] != 0 {
		t.Fatalf("capped %v", capped)
	}

	// a sender's resolved alert the router never held is passed over
	before := len(e.r.alerts)
	_ = e.r.Ingest("flux", []Postable{{Labels: down("never"), StartsAt: e.now.Add(-time.Hour), EndsAt: e.now.Add(-time.Second)}})
	if len(e.r.alerts) != before {
		t.Fatal("an unknown resolved alert was held")
	}
}
