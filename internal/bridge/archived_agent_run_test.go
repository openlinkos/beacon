package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

func TestArchivedIncidentKeepsInFlightHookOutcomeAndProgrammeReport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	receiver := make(chan HookPayload, 2)
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := calls.Add(1)
		var payload HookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		receiver <- payload
		if ordinal == 1 {
			<-release
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer sink.Close()
	defer once.Do(func() { close(release) })
	hookConfig := webhookAgent(sink.URL, false)
	hookConfig.Reporter = "token:runner"
	e.b.d.Config.Agents = []config.Agent{hookConfig}
	e.b.d.Feishu, e.b.d.Bot, e.b.d.Pushover = nil, nil, nil
	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	id := GroupID(firing.GroupKey)
	if err := e.b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	getWake := func() HookPayload {
		t.Helper()
		select {
		case delivery := <-receiver:
			return delivery
		case <-time.After(5 * time.Second):
			t.Fatal("the local receiver never saw the hook")
			return HookPayload{}
		}
	}
	first := getWake()
	who := Actor{Key: hookConfig.Reporter, Name: "runner", Via: "api"}
	start, err := e.b.ReportAgentRun(ctx, id, first.Delivery, who, AgentRunInput{Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.Group(id)
	outcomeID := store.OutcomeID(a.ResolvedAt, id, a.Episode)
	if err := e.b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	second := getWake()
	if first.Delivery == second.Delivery {
		t.Fatal("the receiver did not get distinct deliveries for the recurring group")
	}
	code := 0
	if _, err := e.b.ReportAgentRun(ctx, id, first.Delivery, who, AgentRunInput{Status: "succeeded", ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	e.b.waitWakes()

	archive, err := e.b.IncidentOutcome(outcomeID)
	if err != nil || len(archive.Outcome.AgentDeliveries) != 1 {
		t.Fatalf("the first incident did not retain its delivery: %+v %v", archive, err)
	}
	original := archive.Outcome.AgentDeliveries[0]
	if original.ID != first.Delivery || original.Status != "ok" || original.Attempts != 1 || original.DoneAt.IsZero() ||
		original.Run == nil || original.Run.Status != "succeeded" || !original.StartedAt.Equal(start.At) {
		t.Fatalf("the late HTTP acceptance and terminal report did not coexist: %+v", original)
	}
	g, _ := e.st.Group(id)
	if g.Episode == a.Episode || len(g.Agents) != 1 || g.Agents[0].Delivery != second.Delivery || g.Agents[0].Run != nil {
		t.Fatalf("late reports changed the replacement incident: %+v", g.Agents)
	}
	reachesOrigin := false
	for _, audit := range e.st.AuditLog(20) {
		if audit.Action == "wake_agent" && strings.Contains(audit.Detail, first.Delivery) {
			reachesOrigin = reachesOrigin || audit.Target == outcomeID
			if audit.Target == id {
				t.Fatalf("old hook acceptance entered the recurring group's ordinary audit: %+v", audit)
			}
		}
	}
	if !reachesOrigin {
		t.Fatal("no atomic archival audit for the late HTTP acceptance")
	}
}

func TestArchivedReportRevalidatesCurrentReporterAndRetention(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const reporter = "token:original-runner"
	const delivery = "late-report"
	h := webhookAgent("https://example.com", false)
	h.Severities = nil // No new network traffic in this fixture.
	h.Reporter = reporter
	e.b.d.Config.Agents = []config.Agent{h}
	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	id := GroupID(firing.GroupKey)
	if err := e.b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	g, _ := e.st.Group(id)
	g.Agents = []store.AgentCall{{Hook: h.Name, Event: "firing", Delivery: delivery, Status: "pending", Reporter: reporter}}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if err := e.b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	if err := e.b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	start := AgentRunInput{Status: "running"}
	e.b.d.Config.Agents[0].Reporter = "token:replacement"
	for _, who := range []Actor{{Key: reporter}, {Key: "token:replacement"}} {
		if _, err := e.b.ReportAgentRun(ctx, id, delivery, who, start); !errors.Is(err, ErrAgentRunNotFound) {
			t.Errorf("changing the reporter must not allow touching archived history: %v", err)
		}
	}
	e.b.d.Config.Agents[0].Reporter = reporter
	restored := New(e.b.d)
	who := Actor{Key: reporter, Name: "runner"}
	if _, err := restored.ReportAgentRun(ctx, id, delivery, who, start); err != nil {
		t.Fatalf("the exact binding did not survive a bridge reload: %v", err)
	}
	e.now = e.now.Add(config.DefaultIncidentRetention + time.Hour)
	terminal := AgentRunInput{Status: "timed_out"}
	if _, err := restored.ReportAgentRun(ctx, id, delivery, who, terminal); !errors.Is(err, ErrAgentRunNotFound) {
		t.Fatalf("report could still change a retained row past its query cutoff: %v", err)
	}
	restored.pruneOutcomes()
	if _, err := restored.ReportAgentRun(ctx, id, delivery, who, terminal); !errors.Is(err, ErrAgentRunNotFound) {
		t.Fatalf("report resurrected a pruned row: %v", err)
	}
}
