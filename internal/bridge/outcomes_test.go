package bridge

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

func TestIncidentOutcomesSurviveRecurrenceAndPruning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	firing := hook("firing", "critical", "f1")
	resolved := hook("resolved", "critical", "f1")
	id := GroupID(firing.GroupKey)
	send := func(alertStatus string) {
		t.Helper()
		if alertStatus == "firing" {
			if err := e.b.HandleWebhook(ctx, firing); err != nil {
				t.Fatal(err)
			}
		} else if err := e.b.HandleWebhook(ctx, resolved); err != nil {
			t.Fatal(err)
		}
	}
	send("firing")
	person := ActorOf(store.Principal{User: "alice"}, "console")
	agent := ActorOf(store.Principal{User: "bob", Agent: "duty"}, "mcp")
	if _, err := e.b.Ack(ctx, id, person, "the investigation starts"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.AddNote(ctx, id, agent, "reported findings"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(5 * time.Hour)
	e.b.Tick(ctx) // the acknowledgement has lapsed, but the participation remains evidence
	if g, _ := e.st.Group(id); g.Ack != nil {
		t.Fatal("the fixture must lapse acknowledgement before recovery")
	}
	send("resolved")
	e.now = e.now.Add(time.Minute)
	g, _ := e.st.Group(id)
	e.b.recordProbeRun(id, probeEpisode{starts: g.StartsAt, key: g.Episode}, agent, store.ProbeRun{
		ID: "latest", At: e.now, By: agent.Name, Via: agent.Via,
		Results: []store.ProbeObservation{{Status: "ok"}, {Status: "failed"}, {Status: "missing"}},
	})
	out, err := e.b.IncidentOutcomes(id, "", 1)
	if err != nil || len(out.Outcomes) != 1 {
		t.Fatalf("first outcome: %+v %v", out, err)
	}
	first := out.Outcomes[0]
	if p := first.Participation; len(p) != 2 || p[0].Kind != "agent" || p[0].By != "bob / duty" || p[0].Action != "probe_now" || p[1].Kind != "person" || p[1].By != "alice" {
		t.Fatalf("participation %+v", p)
	}
	if v := first.Verification; v == nil || v.Passed != 1 || v.Failed != 1 || v.Missing != 1 {
		t.Fatalf("verification %+v", v)
	}
	e.now = e.now.Add(2 * time.Minute)
	resumed := e.now
	send("firing")
	e.now = e.now.Add(time.Minute)
	send("resolved")
	newest, err := e.b.IncidentOutcomes(id, "", 1)
	if err != nil || len(newest.Outcomes) != 1 || newest.Before != newest.Outcomes[0].ID || newest.Outcomes[0].ID == first.ID ||
		len(newest.Outcomes[0].Participation) != 0 || newest.Outcomes[0].Verification != nil {
		t.Fatalf("new episode and cursor: %+v %v", newest, err)
	}
	older, err := e.b.IncidentOutcomes(id, newest.Before, 1)
	if err != nil || len(older.Outcomes) != 1 || older.Outcomes[0].ID != first.ID || !older.Outcomes[0].AuditUntil.Before(resumed) || older.Before != "" {
		t.Fatalf("older episode: %+v %v", older, err)
	}
	e.now = e.now.Add(keepResolved + time.Hour)
	e.b.Tick(ctx)
	if _, ok := e.st.Group(id); ok {
		t.Fatal("the rich group outlived its retention")
	}
	if saved, err := e.b.IncidentOutcome(first.ID); err != nil || saved.Outcome.Verification == nil || saved.Outcome.Group != id {
		t.Fatalf("archived outcome: %+v %v", saved, err)
	}
	e.b.d.Config.Storage.IncidentRetention = config.Duration(keepResolved + time.Hour)
	e.now = e.now.Add(time.Hour)
	if _, err := e.b.IncidentOutcome(first.ID); !errors.Is(err, ErrOutcomeNotFound) {
		t.Fatalf("expired outcome returned %v", err)
	}
	e.b.pruneOutcomes()
	got, err := e.st.IncidentOutcomes(store.OutcomeFilter{Limit: 50})
	if err != nil || len(got) != 0 {
		t.Fatalf("physical prune: %+v %v", got, err)
	}
}

func TestIncidentOutcomesBackfillOnlyWhatOldStateShows(t *testing.T) {
	e := newEnv(t)
	id := GroupID(hook("resolved", "critical").GroupKey)
	if err := e.st.PutGroup(store.Group{ID: id, Status: "resolved", Severity: "critical", StartsAt: e.now.Add(-time.Hour), ResolvedAt: e.now,
		Common: map[string]string{"service": "git-example-com"}, Agents: []store.AgentCall{{Event: "firing", Status: "ok"}},
		Notes: []store.Note{{By: "could-be-either", Via: "mcp", At: e.now.Add(-time.Minute)}}}); err != nil {
		t.Fatal(err)
	}
	e.b.initOutcomes()
	p, err := e.b.IncidentOutcomes("", "", 0)
	if err != nil || len(p.Outcomes) != 1 {
		t.Fatalf("backfill: %+v %v", p, err)
	}
	o := p.Outcomes[0]
	if !slices.Equal(o.Services, []string{"git-example-com"}) || o.Verification != nil || len(o.Participation) != 1 || o.Participation[0].Kind != "unattributed" {
		t.Fatalf("backfill guessed identity or a result: %+v", o)
	}
}
