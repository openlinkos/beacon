package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// TestTokensDoNotStopPaging checks that a standalone token's acknowledgement, silence and maintenance
// window behave as an agent's, whether it comes through REST, MCP or the console: they are recorded, but
// the buzz and the Pushover emergency still go out and nothing is muted. A person's acknowledgement, on
// the console, a card or the host's command line, still pauses them.
func TestTokensDoNotStopPaging(t *testing.T) {
	for _, via := range []string{"api", "mcp", "console"} {
		t.Run(via, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			w := hook("firing", "critical", "f1")
			if err := e.b.HandleWebhook(ctx, w); err != nil {
				t.Fatal(err)
			}
			id := GroupID(w.GroupKey)
			token := ActorOf(store.Principal{Token: "oncall-bot", Scopes: []string{"alerts:write", "maintenance:write"}}, via)

			e.now = e.now.Add(time.Minute)
			view, err := e.b.Ack(ctx, id, token, "looking")
			if err != nil || view.Ack == nil || view.Ack.StopsEscalation() {
				t.Fatalf("a token's acknowledgement: %+v %v", view.Ack, err)
			}
			view, err = e.b.Silence(ctx, id, 24*time.Hour, token, "expected while the deploy runs")
			if err != nil || view.Silence == nil || view.Silence.Pauses(e.now) {
				t.Fatalf("a token's silence: %+v %v", view.Silence, err)
			}
			m, err := e.b.ScheduleMaintenance(ctx, token, MaintenanceInput{Services: []string{"git-example-com"}, EndsAt: e.now.Add(time.Hour), Title: "Deploy"})
			if err != nil || !m.ByAgent {
				t.Fatalf("a token's maintenance window: %+v %v", m, err)
			}
			if _, err := e.b.SilenceLabels(ctx, map[string]string{"service": "git-example-com"}, time.Hour, token, "deploy"); err != nil {
				t.Fatal(err)
			}
			if len(e.am.created) != 0 || len(e.am.agent) != 3 {
				t.Errorf("a token's silences muted alerts: muting %v, recorded %v", e.am.created, e.am.agent)
			}
			if got := e.b.Summarize().Acknowledged; got != 0 {
				t.Errorf("a token's acknowledgement counted: %d", got)
			}

			e.now = e.now.Add(15 * time.Minute)
			e.b.Tick(ctx)
			if len(e.fs.buzzes) != 1 || len(e.push.sent) != 1 {
				t.Fatalf("escalation stopped for a token: %d buzzes, %d pages", len(e.fs.buzzes), len(e.push.sent))
			}
			if _, err := e.b.Ack(ctx, id, token, "still on it"); err != nil {
				t.Fatal(err)
			}
			if g, _ := e.st.Group(id); len(e.push.cancelled) != 0 || g.Receipt == "" {
				t.Fatalf("a token cancelled the emergency: cancelled %v, %+v", e.push.cancelled, g)
			}

			// a person on the host's command line takes it over
			view, err = e.b.Ack(ctx, id, Actor{Name: "cli:root", Via: "cli"}, "on it")
			if err != nil || !view.Ack.StopsEscalation() || view.Ack.Kind != "person" || len(e.push.cancelled) != 1 {
				t.Fatalf("a person's acknowledgement on the command line: %+v %v, cancelled %v", view.Ack, err, e.push.cancelled)
			}
			// and a token's later one does not take ownership from them
			if view, _ = e.b.Ack(ctx, id, token, "done"); view.Ack.By != "cli:root" {
				t.Errorf("a token's acknowledgement replaced the person's: %+v", view.Ack)
			}
		})
	}
}

// TestPeoplePausePaging checks the people whose acknowledgements and silences pause notifications to
// people: one signed in with an account, an on-call person on a card or the phone, the command line.
func TestPeoplePausePaging(t *testing.T) {
	for _, who := range []Actor{
		ActorOf(store.Principal{User: "alice"}, "console"),
		{Name: "Alice", Via: "feishu"},
		{Name: "alice", Via: "pushover"},
		{Name: "cli:root", Via: "cli"},
	} {
		if !pausesPeople(who) {
			t.Errorf("%s via %s does not pause paging", who.Name, who.Via)
		}
	}
	for _, who := range []Actor{
		ActorOf(store.Principal{User: "alice", Agent: "oncall"}, "mcp"),
		ActorOf(store.Principal{Token: "oncall-bot"}, "api"),
		ActorOf(store.Principal{Token: "oncall-bot"}, "console"),
		{Name: "claude-code", Via: "mcp"},
		{Name: "oncall-bot", Via: "cli", Key: "token:oncall-bot"},
	} {
		if pausesPeople(who) {
			t.Errorf("%s via %s pauses paging", who.Name, who.Via)
		}
	}
}
