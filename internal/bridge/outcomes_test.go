package bridge

import (
	"context"
	"errors"
	"fmt"
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
	ackAt := e.now
	if _, err := e.b.AddNote(ctx, id, agent, "reported findings"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(5 * time.Hour)
	e.b.Tick(ctx) // the acknowledgement has lapsed, but the participation remains evidence
	if g, _ := e.st.Group(id); g.Ack != nil {
		t.Fatal("the fixture must lapse acknowledgement before recovery")
	}
	if _, err := e.b.AddNote(ctx, id, person, "I handed over the investigation"); err != nil {
		t.Fatal(err)
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
	if p := first.Participation; len(p) != 2 || p[0].Kind != "agent" || p[0].By != "bob / duty" || p[0].Action != "probe_now" || p[1].Kind != "person" || p[1].By != "alice" || p[1].Action != "note" {
		t.Fatalf("participation %+v", p)
	}
	if ack := first.HumanAcknowledgement; ack == nil || !ack.At.Equal(ackAt) || ack.By != "alice" || ack.Via != "console" {
		t.Fatalf("the note replaced the named acknowledgement: %+v", ack)
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
		len(newest.Outcomes[0].Participation) != 0 || newest.Outcomes[0].Verification != nil || newest.Outcomes[0].HasHumanAcknowledgement() {
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

func TestHistoryNamedAcknowledgementExcludesImplicitAndDeviceResponses(t *testing.T) {
	e := newEnv(t)
	g := store.Group{ID: "0123456789ab", Status: "resolved", Severity: "critical"}
	g.StartsAt, g.ResolvedAt = e.now.Add(-time.Hour), e.now
	// Older groups can hold an acknowledgement implicitly created by applying a silence.
	g.Ack = &store.Ack{By: "alice", Kind: "person", Via: "feishu", At: g.StartsAt}
	if o := e.b.outcome(g, e.now, time.Time{}); o == nil || o.HasHumanAcknowledgement() {
		t.Fatalf("an old acknowledgement without action provenance counted as explicit: %+v", o)
	}
	recordParticipation(&g, Actor{Name: "alice", Via: "feishu"}, "silence", g.StartsAt)
	recordParticipation(&g, Actor{Name: "Pushover", Via: "pushover"}, "ack", g.StartsAt.Add(time.Second))
	recordParticipation(&g, Actor{Name: "token-a", Via: "api"}, "ack", g.StartsAt.Add(2*time.Second))
	if o := e.b.outcome(g, e.now, time.Time{}); o == nil || o.HasHumanAcknowledgement() {
		t.Fatalf("silence, device or unattributed action counted as explicit: %+v", o)
	}
	recordParticipation(&g, Actor{Name: "alice", Via: "feishu"}, "ack", g.StartsAt.Add(3*time.Second))
	if o := e.b.outcome(g, e.now, time.Time{}); o == nil || !o.HasHumanAcknowledgement() || o.HumanAcknowledgement.By != "alice" {
		t.Fatalf("explicit acknowledgement missing: %+v", o)
	}
}

func TestHistoryFiltersBeforeLimit(t *testing.T) {
	e := newEnv(t)
	const group = "0123456789ab"
	observed := func(hours int) store.IncidentOutcome {
		at := e.now.Add(-time.Duration(hours) * time.Hour)
		return store.IncidentOutcome{ID: store.OutcomeID(at, group, "legacy"), Group: group, Resolved: at,
			Severity: "critical", HumanAcknowledgement: &store.OutcomeHumanAcknowledgement{By: "alice", Via: "console", At: at}}
	}
	tooOld, older, newest := observed(8*24), observed(30), observed(10)
	glitch, otherSeverity := observed(9), observed(1)
	glitch.HumanAcknowledgement, otherSeverity.Severity = nil, "warning"
	for _, row := range []store.IncidentOutcome{tooOld, older, newest, glitch, otherSeverity} {
		if err := e.st.EnsureIncidentOutcome(row); err != nil {
			t.Fatal(err)
		}
	}
	q := OutcomeQuery{Group: group, Severity: "critical", Since: "7d", Evidence: "human_ack", Limit: 1}
	p := store.Principal{Scopes: []string{"status:read"}}
	first, err := e.b.IncidentOutcomesFor(p, q)
	if err != nil || len(first.Outcomes) != 1 || first.Outcomes[0].ID != newest.ID || first.Before != newest.ID {
		t.Fatalf("filtered first page: %+v %v", first, err)
	}
	q.Before = first.Before
	next, err := e.b.IncidentOutcomesFor(p, q)
	if err != nil || len(next.Outcomes) != 1 || next.Outcomes[0].ID != older.ID || next.Before != "" {
		t.Fatalf("filtered second page: %+v %v", next, err)
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
	if !slices.Equal(o.Services, []string{"git-example-com"}) || o.Verification != nil || o.PassingCheck != nil || len(o.Participation) != 1 || o.Participation[0].Kind != "unattributed" {
		t.Fatalf("backfill guessed identity or a result: %+v", o)
	}
}

func TestIncidentOutcomeBackfillAddsReportsToAnExistingSummary(t *testing.T) {
	e := newEnv(t)
	g := store.Group{ID: GroupID(hook("resolved", "critical").GroupKey), Status: "resolved", Severity: "critical",
		StartsAt: e.now.Add(-time.Minute), ResolvedAt: e.now, Episode: "0123456789abcdef"}
	id := store.OutcomeID(g.ResolvedAt, g.ID, g.Episode)
	old := store.IncidentOutcome{ID: id, Group: g.ID, Resolved: g.ResolvedAt, Title: "Original summary",
		PassingCheck: &store.Verification{ID: "saved-check", Passed: 2}}
	if err := e.st.EnsureIncidentOutcome(old); err != nil {
		t.Fatal(err)
	}
	g.Agents = []store.AgentCall{{Hook: "runner", Event: "firing", Status: "ok",
		Run: &store.AgentRun{Status: "not_started", At: e.now, By: "runner", Via: "api"}}}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	e.b.initOutcomes()
	got, err := e.b.IncidentOutcome(id)
	if err != nil || got.Outcome.Title != old.Title || got.Outcome.PassingCheck == nil || got.Outcome.PassingCheck.ID != "saved-check" ||
		len(got.Outcome.AgentDeliveries) != 1 || got.Outcome.AgentDeliveries[0].Run == nil ||
		got.Outcome.AgentDeliveries[0].Run.Status != "not_started" {
		t.Fatalf("startup backfill changed the prior summary or lost execution evidence: %+v %v", got, err)
	}
}

func TestPassingCheckSurvivesLaterChecksAndRefire(t *testing.T) {
	e := newEnv(t)
	e.b.d.ProbeNow = probeBoard(t)
	ctx := context.Background()
	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	firing.Alerts[0].Labels = map[string]string{"instance": "https://git.example.com/", "module": "http_head"}
	id := GroupID(firing.GroupKey)
	if err := e.b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	answerAs(t, e.b.d.ProbeNow, "us-los-angeles", true)
	answerAs(t, e.b.d.ProbeNow, "cn-shanghai-bgp", true)
	person := ActorOf(store.Principal{User: "alice"}, "console")
	agent := ActorOf(store.Principal{User: "bob", Agent: "duty"}, "mcp")
	e.now = e.now.Add(time.Minute)
	checkedAt := e.now
	passing, err := e.b.ProbeNow(ctx, agent, ProbeNowInput{AlertID: id, Wait: 1})
	if err != nil || passing.Summary != "2 OK" || len(passing.Results) != 2 {
		t.Fatalf("check fixture: %+v, %v", passing, err)
	}
	g, _ := e.st.Group(id)
	episode := probeEpisode{starts: g.StartsAt, key: g.Episode}
	e.now = e.now.Add(time.Minute)
	if err := e.b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	// The next shift keeps checking until rich history has dropped the original pass.
	for i := 0; i < keepProbeRuns; i++ {
		e.now = e.now.Add(time.Minute)
		e.b.recordProbeRun(id, episode, person, store.ProbeRun{ID: fmt.Sprintf("incomplete-%d", i), At: e.now, By: person.Name, Via: person.Via,
			Results: []store.ProbeObservation{
				{Status: "ok", Location: "us-los-angeles", Instance: "https://git.example.com/", Module: "http_head"},
				{Status: "missing", Location: "cn-shanghai-bgp", Instance: "https://git.example.com/", Module: "http_head"},
			}})
	}
	if g, _ = e.st.Group(id); len(g.ProbeRuns) != keepProbeRuns || g.ProbeRuns[0].ID != "incomplete-0" {
		t.Fatalf("the passing detail must have been clipped: %+v", g.ProbeRuns)
	}
	outcomes, err := e.b.IncidentOutcomes(id, "", 1)
	if err != nil || len(outcomes.Outcomes) != 1 {
		t.Fatalf("resolved incident: %+v %v", outcomes, err)
	}
	outcome := outcomes.Outcomes[0].ID
	assertChecks := func() {
		t.Helper()
		result, err := e.b.IncidentOutcome(outcome)
		if err != nil {
			t.Fatal(err)
		}
		if check := result.Outcome.PassingCheck; check == nil || check.ID != passing.ID || !check.At.Equal(checkedAt) ||
			check.Passed != 2 || check.Failed != 0 || check.Missing != 0 || check.ActorKind != "agent" || check.By != agent.Name || check.Via != agent.Via {
			t.Fatalf("retained pass %+v", check)
		}
		if latest := result.Outcome.Verification; latest == nil || latest.ID != fmt.Sprintf("incomplete-%d", keepProbeRuns-1) ||
			latest.Passed != 1 || latest.Failed != 0 || latest.Missing != 1 || latest.ActorKind != "person" || latest.By != person.Name || latest.Via != person.Via {
			t.Fatalf("latest raw check %+v", latest)
		}
	}
	assertChecks()
	e.now = e.now.Add(time.Minute)
	if err := e.b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	e.b.recordProbeRun(id, episode, agent, store.ProbeRun{ID: "late-old-episode", At: e.now, By: agent.Name, Via: agent.Via,
		Results: []store.ProbeObservation{{Status: "ok", Location: "us-los-angeles", Instance: "https://git.example.com/"}}})
	if g, _ = e.st.Group(id); g.PassingCheck != nil || len(g.ProbeRuns) != 0 {
		t.Fatalf("a prior incident's check leaked to the new incident: %+v %+v", g.PassingCheck, g.ProbeRuns)
	}
	assertChecks()
	e.now = e.now.Add(time.Minute)
	if err := e.b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(keepResolved + time.Hour)
	e.b.Tick(ctx)
	if _, ok := e.st.Group(id); ok {
		t.Fatal("the rich group must have been removed")
	}
	assertChecks()
}

func TestPassingCheckBackfillsOnlyRetainedUnambiguousDetails(t *testing.T) {
	e := newEnv(t)
	id := GroupID(hook("resolved", "critical").GroupKey)
	old := store.IncidentOutcome{ID: store.OutcomeID(e.now, id, "legacy"), Group: id, Started: e.now.Add(-time.Hour), Resolved: e.now,
		Title: "Retained outcome", Verification: &store.Verification{ID: "latest", Missing: 1}, Sessions: []string{"recorded-session"}}
	if err := e.st.EnsureIncidentOutcome(old); err != nil {
		t.Fatal(err)
	}
	e.b.initOutcomes()
	got, err := e.b.IncidentOutcome(old.ID)
	if err != nil || got.Outcome.PassingCheck != nil {
		t.Fatalf("old compact outcome gained evidence without its detail: %+v %v", got, err)
	}
	if err := e.st.PutGroup(store.Group{ID: id, Status: "resolved", StartsAt: old.Started, ResolvedAt: old.Resolved,
		ProbeRuns: []store.ProbeRun{
			{ID: "passing", By: "untyped", At: e.now.Add(-time.Minute), Results: []store.ProbeObservation{
				{Status: "ok", Location: "edge-1", Instance: "https://git.example.com/"},
			}},
			{ID: "ambiguous", At: e.now, Results: []store.ProbeObservation{
				{Status: "ok", Location: "edge-1", Instance: "https://git.example.com/"},
				{Status: "ok", Location: "edge-1", Instance: "https://git.example.com/"},
			}},
		}}); err != nil {
		t.Fatal(err)
	}
	e.b.initOutcomes()
	got, err = e.b.IncidentOutcome(old.ID)
	if err != nil || got.Outcome.Title != old.Title || !slices.Equal(got.Outcome.Sessions, old.Sessions) || got.Outcome.Verification == nil || *got.Outcome.Verification != *old.Verification {
		t.Fatalf("startup changed existing outcome fields: %+v %v", got, err)
	}
	if check := got.Outcome.PassingCheck; check == nil || check.ID != "passing" || check.Passed != 1 || check.By != "untyped" || check.ActorKind != "" {
		t.Fatalf("retained legacy passing check %+v", check)
	}
}
