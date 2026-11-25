package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

func TestAgentRunReportSurvivesTheHookResponse(t *testing.T) {
	e := newEnv(t)
	const reporter = "token:runner"
	who := Actor{Key: reporter, Name: "runner", Via: "api"}
	code := 79
	ctx := context.Background()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p HookPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, in := range []AgentRunInput{{Status: "running"}, {Status: "failed", ExitCode: &code}} {
			if _, err := e.b.ReportAgentRun(r.Context(), p.Alert.ID, p.Delivery, who, in); err != nil {
				t.Errorf("report before sending the hook response: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()
	a := webhookAgent(receiver.URL, false)
	a.Reporter = reporter
	e.b.d.Config.Agents = []config.Agent{a}
	wake := hook("firing", "critical", "f1")
	if err := e.b.HandleWebhook(ctx, wake); err != nil {
		t.Fatal(err)
	}
	e.b.waitWakes()
	id := GroupID(wake.GroupKey)
	g, ok := e.st.Group(id)
	if !ok || !g.Firing() || g.Ack != nil || g.Silence != nil || len(g.Participation) != 0 || len(g.Agents) != 1 {
		t.Fatalf("execution reports changed operational state: %+v", g)
	}
	c := g.Agents[0]
	if c.Status != "ok" || c.Code != http.StatusAccepted || c.RunStart == nil || c.RunStart.Status != "running" ||
		c.Run == nil || c.Run.Status != "failed" || runExit(c.Run.ExitCode) != 79 {
		t.Fatalf("the transport response overwrote execution: %+v", c)
	}
	for _, repeat := range []AgentRunInput{{Status: "running"}, {Status: "failed", ExitCode: &code}} {
		run, err := e.b.ReportAgentRun(ctx, id, c.Delivery, who, repeat)
		if err != nil || run.Status != "failed" || !run.At.Equal(c.Run.At) {
			t.Fatalf("repeated report changed the result: %+v %v", run, err)
		}
	}
	if _, err := e.b.ReportAgentRun(ctx, id, c.Delivery, who, AgentRunInput{Status: "timed_out"}); !errors.Is(err, ErrAgentRunConflict) {
		t.Fatalf("terminal report can be changed: %v", err)
	}
	if err := e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	timeline, err := e.b.Timeline(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	var runs []Event
	for _, event := range timeline.Events {
		if event.Kind == "agent_run" && event.ID == c.Delivery {
			runs = append(runs, event)
		}
	}
	if len(runs) != 2 || runs[0].Result != "running" || runs[1].Result != "failed" || runExit(runs[1].Exit) != 79 || runs[1].Via != "api" {
		t.Fatalf("resolved episode lacked execution evidence: %+v", runs)
	}
	var audit int
	for _, e := range e.st.AuditLog(20) {
		if e.Action == "report_agent_run" && e.Actor == who.Name && e.Target == id {
			audit++
		}
	}
	if audit != 2 {
		t.Fatalf("expected one audit entry per changed phase, got %d", audit)
	}
}

func TestIncidentOutcomeRetainsReportsAfterResolution(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const reporter = "token:runner"
	const episode = "0123456789abcdef"
	h := webhookAgent("https://example.com", false)
	h.Reporter = reporter
	e.b.d.Config.Agents = []config.Agent{h}
	resolved := hook("resolved", "critical", "f1")
	id := GroupID(resolved.GroupKey)
	c := store.AgentCall{
		Hook: h.Name, Kind: "webhook", Event: "firing", Delivery: "1111222233334444", Status: "pending",
		Reporter: reporter, At: e.now.Add(-2 * time.Minute),
	}
	if err := e.st.PutGroup(store.Group{ID: id, Key: resolved.GroupKey, Status: "firing", Severity: "critical",
		StartsAt: e.now.Add(-3 * time.Minute), Episode: episode, Agents: []store.AgentCall{c}}); err != nil {
		t.Fatal(err)
	}
	if err := e.b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	outcomeID := store.OutcomeID(e.now, id, episode)
	delivery := func() store.OutcomeAgentDelivery {
		t.Helper()
		page, err := e.b.IncidentOutcomes(id, "", 1)
		got, detailErr := e.b.IncidentOutcome(outcomeID)
		if err != nil || detailErr != nil || len(page.Outcomes) != 1 || len(page.Outcomes[0].AgentDeliveries) != 1 ||
			len(got.Outcome.AgentDeliveries) != 1 || page.Outcomes[0].AgentDeliveries[0].ID != c.Delivery {
			t.Fatalf("retained delivery: %+v %+v (%v, %v)", page, got, err, detailErr)
		}
		return got.Outcome.AgentDeliveries[0]
	}
	if got := delivery(); got.Status != "pending" || got.Run != nil || got.Hook != h.Name {
		t.Fatalf("initial recovery evidence: %+v", got)
	}
	who := Actor{Key: reporter, Name: "runner", Via: "api"}
	e.now = e.now.Add(5 * time.Second)
	start := e.now
	if _, err := e.b.ReportAgentRun(ctx, id, c.Delivery, who, AgentRunInput{Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if got := delivery(); got.Status != "pending" || got.Run == nil || got.Run.Status != "running" || !got.StartedAt.Equal(start) {
		t.Fatalf("late start report: %+v", got)
	}
	e.now = e.now.Add(5 * time.Second)
	c.Status, c.Done, c.Attempts = "ok", e.now, 2
	e.b.recordCall(ctx, id, c)
	if got := delivery(); got.Status != "ok" || !got.DoneAt.Equal(c.Done) || got.Attempts != 2 ||
		got.Run == nil || got.Run.Status != "running" {
		t.Fatalf("late acknowledgement replaced the programme report: %+v", got)
	}
	e.now = e.now.Add(time.Minute)
	code := 79
	if _, err := e.b.ReportAgentRun(ctx, id, c.Delivery, who, AgentRunInput{Status: "failed", ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	assertTerminal := func() {
		t.Helper()
		d := delivery()
		if d.Status != "ok" || d.Run == nil || d.Run.Status != "failed" || runExit(d.Run.ExitCode) != code ||
			d.Run.By != who.Name || !d.StartedAt.Equal(start) {
			t.Fatalf("archive lost transport or execution evidence: %+v", d)
		}
	}
	assertTerminal()
	e.b.d.Config.Agents = nil
	e.now = e.now.Add(time.Minute)
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	assertTerminal()
	g, _ := e.st.Group(id)
	if g.Episode == episode || len(g.Agents) != 0 {
		t.Fatalf("new episode inherited deliveries: %+v", g)
	}
}
