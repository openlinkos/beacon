package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

func waits(d time.Duration) *config.Duration {
	c := config.Duration(d)
	return &c
}

type resolvedHookAttempt struct {
	Header http.Header
	Body   []byte
}

// resolvedHookReceiver records requests without network I/O in the synctest bubble.
type resolvedHookReceiver chan resolvedHookAttempt

func (h resolvedHookReceiver) RoundTrip(req *http.Request) (*http.Response, error) {
	defer func() { _ = req.Body.Close() }()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	h <- resolvedHookAttempt{Header: req.Header.Clone(), Body: body}
	return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody}, nil
}

func resolvedHookEnv(t *testing.T, delay time.Duration) (*env, resolvedHookReceiver) {
	t.Helper()
	e := newEnv(t)
	start := time.Now()
	e.b.d.Now = func() time.Time { return e.now.Add(time.Since(start)) }
	e.b.d.Config.PublicURL = "https://status.example.com"
	agent := webhookAgent("https://hooks.example.com", true)
	agent.ResolvedAfter = waits(delay)
	e.b.d.Config.Agents = []config.Agent{agent}
	receiver := make(resolvedHookReceiver, 4)
	e.b.hooks.init()
	e.b.hooks.hc.Transport = receiver
	return e, receiver
}

// The resolved call waits the hook's resolved_after, so that the closing note the agent's first run
// writes right after its fix is on the timeline when the agent hears of the end; and it tells the agent
// which call woke it, and whether that run may still be going.
func TestResolvedCallWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const delay, closing = time.Minute, 20 * time.Second
		e, receiver := resolvedHookEnv(t, delay)
		ctx := context.Background()
		id := GroupID(hook("firing", "critical").GroupKey)
		if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(receiver) != 1 {
			t.Fatalf("the firing call goes at once: %d calls", len(receiver))
		}
		firing := (<-receiver).Header.Get("X-Beacon-Delivery")

		synctest.Sleep(3 * time.Minute)
		resolvedAt := e.b.now()
		if err := e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1")); err != nil {
			t.Fatal(err)
		}
		g, _ := e.st.Group(id)
		due := resolvedAt.Add(delay)
		if len(g.Agents) != 2 || g.Agents[1].Status != "pending" || !g.Agents[1].Due.Equal(due) {
			t.Fatalf("the resolved call waits: %+v", g.Agents)
		}
		synctest.Sleep(closing)
		if _, err := e.b.AddNote(ctx, id, Actor{Name: "claude-code", Via: "mcp"}, "restarted the timer; resolved"); err != nil {
			t.Fatal(err)
		}
		synctest.Sleep(delay - closing - time.Nanosecond)
		if len(receiver) != 0 {
			t.Fatalf("the resolved call went before it was due: %d calls", len(receiver))
		}
		synctest.Sleep(time.Nanosecond)
		if len(receiver) != 1 {
			t.Fatalf("the resolved call was not made when due: %d calls", len(receiver))
		}
		attempt := <-receiver
		if event := attempt.Header.Get("X-Beacon-Event"); event != "resolved" {
			t.Fatalf("the call at its due time sent event %q", event)
		}
		body := attempt.Body
		var p HookPayload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		if p.Event != "resolved" || !p.At.Equal(resolvedAt) || p.Alert.Status != "resolved" || p.Hint != hintResolvedSoon || p.FiringCall == nil ||
			p.FiringCall.Delivery != firing || p.FiringCall.Status != "ok" || !p.FiringCall.Recent || !p.FiringCall.At.Equal(resolvedAt.Add(-3*time.Minute)) {
			t.Fatalf("resolved payload: %+v %+v", p, p.FiringCall)
		}
		if !strings.Contains(p.Hint, "may still be finishing its notes") || !strings.Contains(string(body), `"firing_call":{"delivery":"`+firing+`"`) {
			t.Errorf("hint and payload: %s", body)
		}
		if g, _ = e.st.Group(id); g.Agents[1].Status != "ok" || !g.Agents[1].Done.Equal(due) {
			t.Errorf("the call is recorded at its due time: %+v", g.Agents[1])
		}
		tl, err := e.b.Timeline(ctx, id, "")
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, ev := range tl.Events {
			kinds = append(kinds, ev.Kind)
		}
		if got := strings.Join(kinds, " "); got != "started notified agent resolved notification note agent" {
			t.Errorf("the resolved hook delivery follows the agent's note: %s", got)
		}
	})
}

// A group that resolves long after its firing call says so: the earlier run is over.
func TestResolvedCallLate(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 202)
	e.b.d.Config.PublicURL = "https://status.example.com"
	a := webhookAgent(hs.srv.URL, true)
	a.ResolvedAfter = waits(0) // at once
	e.b.d.Config.Agents = []config.Agent{a}
	ctx := context.Background()
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	e.b.waitWakes()
	e.now = e.now.Add(time.Hour)
	_ = e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1"))
	g, _ := e.st.Group(GroupID(hook("firing", "critical").GroupKey))
	if !g.Agents[1].Due.IsZero() {
		t.Errorf("resolved_after 0 sends at once: %+v", g.Agents[1])
	}
	e.b.waitWakes()
	var p HookPayload
	if len(hs.bodies) != 2 || json.Unmarshal(hs.bodies[1], &p) != nil {
		t.Fatalf("calls: %d", len(hs.bodies))
	}
	if p.Hint != hintResolved || p.FiringCall == nil || p.FiringCall.Recent {
		t.Errorf("an hour after the firing call: %+v %+v", p, p.FiringCall)
	}
}

// A group that fires again while the resolved call waits is a new episode: the hook is woken for it, and
// the resolved call of the episode before is not made.
func TestResolvedCallDroppedWhenFiringAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const delay = time.Minute
		e, receiver := resolvedHookEnv(t, delay)
		ctx := context.Background()
		if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1")); err != nil {
			t.Fatal(err)
		}
		synctest.Sleep(delay / 2)
		if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f2")); err != nil {
			t.Fatal(err)
		}
		synctest.Sleep(delay / 2)
		if len(receiver) != 2 {
			t.Fatalf("two firing calls and no resolved one: %d calls", len(receiver))
		}
		first, second := (<-receiver).Header, (<-receiver).Header
		if first.Get("X-Beacon-Event") != "firing" || second.Get("X-Beacon-Event") != "firing" ||
			first.Get("X-Beacon-Delivery") == second.Get("X-Beacon-Delivery") {
			t.Fatalf("two separate firing calls: %+v and %+v", first, second)
		}
		found := false
		for _, a := range e.st.AuditLog(10) {
			found = found || (a.Action == "wake_agent" && strings.Contains(a.Detail, "(webhook, resolved): not made"))
		}
		if !found {
			t.Errorf("the audit log says the call was not made: %+v", e.st.AuditLog(10))
		}
	})
}

// A restart takes up a resolved call that waits, and makes it when it is due.
func TestResumeWaitingCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const delay, elapsed = time.Minute, 40 * time.Second
		e, receiver := resolvedHookEnv(t, delay)
		restartedAt := e.b.now()
		resolvedAt := restartedAt.Add(-elapsed)
		due := resolvedAt.Add(delay)
		g := store.Group{ID: "g1", Status: "resolved", Severity: "critical", StartsAt: restartedAt.Add(-time.Hour), ResolvedAt: resolvedAt,
			Labels: map[string]string{"alertname": "ServiceDown", "service": "git-a"},
			Agents: []store.AgentCall{
				{Hook: "claude-oncall", Kind: "webhook", Event: "firing", Delivery: "woke", Status: "ok", At: restartedAt.Add(-5 * time.Minute)},
				{Hook: "claude-oncall", Kind: "webhook", Event: "resolved", Delivery: "due", Status: "pending", At: resolvedAt, Due: due},
			}}
		if err := e.st.PutGroup(g); err != nil {
			t.Fatal(err)
		}
		e.b.resumeWakes(context.Background())
		synctest.Sleep(delay - elapsed - time.Nanosecond)
		if len(receiver) != 0 {
			t.Fatalf("the resumed call went before its persisted due time: %d calls", len(receiver))
		}
		synctest.Sleep(time.Nanosecond)
		if len(receiver) != 1 {
			t.Fatalf("the resumed call was not made at its persisted due time: %d calls", len(receiver))
		}
		attempt := <-receiver
		if delivery := attempt.Header.Get("X-Beacon-Delivery"); delivery != "due" {
			t.Fatalf("the call at its due time sent delivery %q", delivery)
		}
		var p HookPayload
		if err := json.Unmarshal(attempt.Body, &p); err != nil {
			t.Fatal(err)
		}
		if !p.At.Equal(resolvedAt) || p.FiringCall == nil || p.FiringCall.Delivery != "woke" || !p.FiringCall.Recent {
			t.Errorf("payload: %+v %+v", p, p.FiringCall)
		}
		if g, _ = e.st.Group(g.ID); g.Agents[1].Status != "ok" || !g.Agents[1].Done.Equal(due) {
			t.Errorf("the call is recorded at its original due time: %+v", g.Agents[1])
		}
	})
}
