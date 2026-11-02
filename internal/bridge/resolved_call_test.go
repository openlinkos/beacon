package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

func waits(d time.Duration) *config.Duration {
	c := config.Duration(d)
	return &c
}

// The resolved call waits the hook's resolved_after, so that the closing note the agent's first run
// writes right after its fix is on the timeline when the agent hears of the end; and it tells the agent
// which call woke it, and whether that run may still be going.
func TestResolvedCallWaits(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 202)
	e.b.d.Config.PublicURL = "https://status.example.com"
	a := webhookAgent(hs.srv.URL, true)
	a.ResolvedAfter = waits(200 * time.Millisecond)
	e.b.d.Config.Agents = []config.Agent{a}
	ctx := context.Background()
	id := GroupID(hook("firing", "critical").GroupKey)
	if err := e.b.HandleWebhook(ctx, hook("firing", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	e.b.waitWakes()
	if len(hs.reqs) != 1 {
		t.Fatalf("the firing call goes at once: %d calls", len(hs.reqs))
	}
	firing := hs.reqs[0].Header.Get("X-Beacon-Delivery")

	e.now = e.now.Add(3 * time.Minute)
	resolved := time.Now()
	if err := e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1")); err != nil {
		t.Fatal(err)
	}
	g, _ := e.st.Group(id)
	if len(g.Agents) != 2 || g.Agents[1].Status != "pending" || !g.Agents[1].Due.Equal(e.now.Add(200*time.Millisecond)) {
		t.Fatalf("the resolved call waits: %+v", g.Agents)
	}
	// the first run's closing note, written while the call waits
	resolvedAt := e.now
	e.now = e.now.Add(20 * time.Second)
	if _, err := e.b.AddNote(ctx, id, Actor{Name: "claude-code", Via: "mcp"}, "restarted the timer; resolved"); err != nil {
		t.Fatal(err)
	}
	hs.mu.Lock()
	early := len(hs.reqs)
	hs.mu.Unlock()
	if early != 1 {
		t.Fatalf("the resolved call went before it was due: %d calls", early)
	}
	e.b.waitWakes()
	if len(hs.reqs) != 2 || hs.reqs[1].Header.Get("X-Beacon-Event") != "resolved" {
		t.Fatalf("resolved: %d calls", len(hs.reqs))
	}
	if waited := time.Since(resolved); waited < 200*time.Millisecond {
		t.Errorf("the call waited only %v", waited)
	}
	var p HookPayload
	if err := json.Unmarshal(hs.bodies[1], &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != "resolved" || !p.At.Equal(resolvedAt) || p.Alert.Status != "resolved" || p.Hint != hintResolvedSoon || p.FiringCall == nil ||
		p.FiringCall.Delivery != firing || p.FiringCall.Status != "ok" || !p.FiringCall.Recent || !p.FiringCall.At.Equal(resolvedAt.Add(-3*time.Minute)) {
		t.Fatalf("resolved payload: %+v %+v", p, p.FiringCall)
	}
	if !strings.Contains(p.Hint, "may still be finishing its notes") || !strings.Contains(string(hs.bodies[1]), `"firing_call":{"delivery":"`+firing+`"`) {
		t.Errorf("hint and payload: %s", hs.bodies[1])
	}
	if g, _ = e.st.Group(id); g.Agents[1].Status != "ok" {
		t.Errorf("the call is recorded: %+v", g.Agents[1])
	}
	tl, err := e.b.Timeline(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range tl.Events {
		kinds = append(kinds, ev.Kind)
	}
	if got := strings.Join(kinds, " "); got != "started notified agent agent resolved note" {
		t.Errorf("the note is on the timeline: %s", got)
	}
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
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 202)
	e.b.d.Config.PublicURL = "https://status.example.com"
	a := webhookAgent(hs.srv.URL, true)
	a.ResolvedAfter = waits(150 * time.Millisecond)
	e.b.d.Config.Agents = []config.Agent{a}
	ctx := context.Background()
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f1"))
	e.b.waitWakes()
	_ = e.b.HandleWebhook(ctx, hook("resolved", "critical", "f1"))
	e.now = e.now.Add(30 * time.Second)
	_ = e.b.HandleWebhook(ctx, hook("firing", "critical", "f2"))
	e.b.waitWakes()
	if len(hs.reqs) != 2 || hs.reqs[0].Header.Get("X-Beacon-Event") != "firing" || hs.reqs[1].Header.Get("X-Beacon-Event") != "firing" {
		t.Fatalf("two firing calls and no resolved one: %d", len(hs.reqs))
	}
	found := false
	for _, a := range e.st.AuditLog(10) {
		found = found || (a.Action == "wake_agent" && strings.Contains(a.Detail, "(webhook, resolved): not made"))
	}
	if !found {
		t.Errorf("the audit log says the call was not made: %+v", e.st.AuditLog(10))
	}
}

// A restart takes up a resolved call that waits, and makes it when it is due.
func TestResumeWaitingCall(t *testing.T) {
	fastRetries(t)
	e := newEnv(t)
	hs := newHookServer(t, 202)
	e.b.d.Config.PublicURL = "https://status.example.com"
	a := webhookAgent(hs.srv.URL, true)
	a.ResolvedAfter = waits(time.Minute)
	e.b.d.Config.Agents = []config.Agent{a}
	resolved := e.now.Add(-time.Minute)
	g := store.Group{ID: "g1", Status: "resolved", Severity: "critical", StartsAt: e.now.Add(-time.Hour), ResolvedAt: resolved,
		Labels: map[string]string{"alertname": "ServiceDown", "service": "git-a"},
		Agents: []store.AgentCall{
			{Hook: "claude-oncall", Kind: "webhook", Event: "firing", Delivery: "woke", Status: "ok", At: e.now.Add(-5 * time.Minute)},
			{Hook: "claude-oncall", Kind: "webhook", Event: "resolved", Delivery: "due", Status: "pending", At: resolved, Due: e.now.Add(100 * time.Millisecond)},
		}}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	e.b.resumeWakes(context.Background())
	e.b.waitWakes()
	if len(hs.reqs) != 1 || hs.reqs[0].Header.Get("X-Beacon-Delivery") != "due" {
		t.Fatalf("the waiting call is made: %d calls", len(hs.reqs))
	}
	var p HookPayload
	_ = json.Unmarshal(hs.bodies[0], &p)
	if !p.At.Equal(resolved) || p.FiringCall == nil || p.FiringCall.Delivery != "woke" || !p.FiringCall.Recent {
		t.Errorf("payload: %+v %+v", p, p.FiringCall)
	}
}
