package bridge

import (
	"errors"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

func TestStartedEpisodePagesSurviveRecoveryAndRecurrence(t *testing.T) {
	e := newEnv(t)
	fired := hook("firing", "critical", "fixture")
	resolved := hook("resolved", "warning", "fixture")
	id := GroupID(fired.GroupKey)

	fireAt := func(start time.Time) {
		t.Helper()
		fired.Alerts[0].StartsAt = start
		if err := e.b.HandleWebhook(t.Context(), fired); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func() {
		t.Helper()
		if err := e.b.HandleWebhook(t.Context(), resolved); err != nil {
			t.Fatal(err)
		}
	}
	firstStart := e.now.Add(-25 * time.Hour)
	fireAt(firstStart)
	first, _ := e.st.Group(id)
	resolve()
	secondStart := e.now.Add(-2 * time.Minute)
	fireAt(secondStart)
	second, _ := e.st.Group(id)
	if first.Episode == second.Episode {
		t.Fatal("fixture did not recur")
	}

	q := IncidentEpisodeQuery{Group: id, Severity: "critical", Since: firstStart.Format(time.RFC3339),
		Until: e.now.Format(time.RFC3339), Limit: 1}
	page, err := e.b.IncidentEpisodes(t.Context(), q)
	if err != nil || len(page.Episodes) != 1 || page.Before == "" || page.Episodes[0].Episode != second.Episode ||
		page.Episodes[0].State != "open" || page.Episodes[0].Resolved != nil || page.Episodes[0].OpeningSeverity != "critical" {
		t.Fatalf("the newest open critical episode is missing: %+v %v", page, err)
	}

	// A rule recovery changes the second row's state without moving it across the start-time cursor.
	e.now = e.now.Add(24 * time.Hour)
	resolve()
	fireAt(e.now.Add(-time.Minute))
	q.Before = page.Before
	next, err := e.b.IncidentEpisodes(t.Context(), q)
	if err != nil || len(next.Episodes) != 1 || next.Before != "" || next.Episodes[0].Episode != first.Episode ||
		next.Episodes[0].State != "resolved" || next.Episodes[0].Severity != "warning" || next.Episodes[0].OpeningSeverity != "critical" {
		t.Fatalf("cursor moved over a recovered or recurring episode: %+v %v", next, err)
	}
	q.Before = ""
	latest, err := e.b.IncidentEpisodes(t.Context(), q)
	if err != nil || len(latest.Episodes) != 1 || latest.Episodes[0].Episode != second.Episode ||
		latest.Episodes[0].State != "resolved" || latest.Episodes[0].OutcomeID == "" ||
		latest.Before != page.Before || latest.Until != page.Until {
		t.Fatalf("fixed window or stable episode identity changed on recovery: %+v %v", latest, err)
	}
}

func TestStartedEpisodeWindowReportsItsRetainedBoundary(t *testing.T) {
	e := newEnv(t)
	e.b.d.Config.Storage.IncidentRetention = config.Duration(time.Hour)
	legacyStart := e.now.Add(-2 * time.Hour)
	const legacyGroup = "0123456789ab"
	if err := e.st.PutGroup(store.Group{ID: legacyGroup, StartsAt: legacyStart, Status: "firing", Severity: "critical"}); err != nil {
		t.Fatal(err)
	}
	resolved := legacyStart.Add(time.Minute)
	if err := e.st.EnsureIncidentOutcome(store.IncidentOutcome{
		ID: store.OutcomeID(resolved, "0123456789ac", "legacy"), Group: "0123456789ac", Started: legacyStart,
		Resolved: resolved, Severity: "critical",
	}); err != nil {
		t.Fatal(err)
	}
	q := IncidentEpisodeQuery{Since: legacyStart.Format(time.RFC3339), Severity: "critical"}
	page, err := e.b.IncidentEpisodes(t.Context(), q)
	if err != nil || len(page.Episodes) != 1 || page.Episodes[0].Group != legacyGroup || page.Episodes[0].Origin != "unknown" ||
		page.Episodes[0].OpeningSeverity != "unknown" || page.Episodes[0].Wake != "unknown" ||
		!page.RetainedFrom.Equal(e.now.Add(-time.Hour)) {
		t.Fatalf("open legacy data or the retention boundary was obscured: %+v %v", page, err)
	}
	for _, invalid := range []IncidentEpisodeQuery{
		{Until: e.now.Add(time.Second).Format(time.RFC3339)},
		{Since: e.now.Add(-32 * 24 * time.Hour).Format(time.RFC3339)},
		{Since: "seven days"},
		{Before: store.EpisodeCursorPrefix + store.OutcomeID(legacyStart, legacyGroup, "legacy")},
	} {
		if _, err := e.b.IncidentEpisodes(t.Context(), invalid); !errors.Is(err, ErrBadInput) {
			t.Errorf("invalid snapshot input: %+v %v", invalid, err)
		}
	}
}
