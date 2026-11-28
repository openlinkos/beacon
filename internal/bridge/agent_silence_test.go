package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// TestAgentSilenceKeepsEscalating checks that an agent's silence, like its acknowledgement, leaves the
// escalation to people in force: the buzz and the Pushover emergency still go out, and an emergency in
// flight is not cancelled. Only a person's silence pauses them.
func TestAgentSilenceKeepsEscalating(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := hook("firing", "critical", "f1")
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	id := GroupID(w.GroupKey)
	agent := ActorOf(store.Principal{User: "alex", Agent: "oncall"}, "mcp")

	e.now = e.now.Add(time.Minute)
	view, err := e.b.Silence(ctx, id, 24*time.Hour, agent, "expected while the deploy runs")
	if err != nil || view.Silence == nil || view.Silence.Kind != "agent" {
		t.Fatalf("an agent's silence: %+v %v", view.Silence, err)
	}
	if len(e.am.created) != 0 || len(e.am.agent) != 1 {
		t.Errorf("an agent's silence was made to mute alerts: muting %v, recorded %v", e.am.created, e.am.agent)
	}
	if view.Ack == nil || view.Ack.StopsEscalation() {
		t.Errorf("an agent's silence took ownership: %+v", view.Ack)
	}

	e.now = e.now.Add(15 * time.Minute)
	e.b.Tick(ctx)
	if len(e.fs.buzzes) != 1 || len(e.push.sent) != 1 {
		t.Fatalf("escalation stopped under an agent's silence: %d buzzes, %d pages", len(e.fs.buzzes), len(e.push.sent))
	}
	// a second agent silence does not cancel the emergency in flight either
	if _, err := e.b.Silence(ctx, id, time.Hour, agent, "still expected"); err != nil {
		t.Fatal(err)
	}
	g, _ := e.st.Group(id)
	if len(e.push.cancelled) != 0 || g.Receipt == "" || g.Pushed.IsZero() {
		t.Fatalf("an agent's silence cancelled the emergency: cancelled %v, %+v", e.push.cancelled, g)
	}
	card := cardText(e.b.card(ctx, &g))
	if !strings.Contains(card, "Silenced by agent alex / oncall") || !strings.Contains(card, "people are still notified") ||
		!strings.Contains(card, `"a":"silence"`) {
		t.Errorf("the card under an agent's silence: %s", card)
	}

	// a person's silence pauses escalation and cancels the emergency
	person := Actor{Name: "alice", Via: "console", Key: "user:alice"}
	view, err = e.b.Silence(ctx, id, time.Hour, person, "maintenance window")
	if err != nil || view.Silence.Kind != "person" || len(e.am.created) != 1 || len(e.push.cancelled) != 1 {
		t.Fatalf("a person's silence: %+v %v, muting %v, cancelled %v", view.Silence, err, e.am.created, e.push.cancelled)
	}
	e.now = e.now.Add(30 * time.Minute)
	e.b.Tick(ctx)
	if len(e.push.sent) != 1 {
		t.Error("paged during a person's silence")
	}
}

// TestAgentSilenceNeedsAgentSilencer checks that an agent's silence is refused, rather than muting
// alerts, when the silencer cannot keep it apart.
func TestAgentSilenceNeedsAgentSilencer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := hook("firing", "critical", "f1")
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	e.b.d.AM = plainSilencer{Silencer: e.am}
	agent := ActorOf(store.Principal{User: "alex", Agent: "oncall"}, "mcp")
	if _, err := e.b.Silence(ctx, GroupID(w.GroupKey), time.Hour, agent, "expected"); err == nil || len(e.am.created) != 0 {
		t.Fatalf("an agent's silence went to a plain silencer: %v %v", err, e.am.created)
	}
}

// plainSilencer offers only what Silencer has.
type plainSilencer struct{ Silencer }
