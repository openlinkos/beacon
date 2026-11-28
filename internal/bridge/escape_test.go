package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

// mention and fakeLink are what text from outside Beacon would use to page everyone in the chat, or to put a
// button-like link of its own on a card.
const (
	mention  = `<at id=all></at><at user_id="all"></at>`
	fakeLink = `[Approve the fix](https://evil.example/approve)`
)

// markdownOf is the text of every markdown element of a card.
func markdownOf(t *testing.T, c feishu.Card) []string {
	t.Helper()
	b, _ := json.Marshal(c)
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch x := x.(type) {
		case map[string]any:
			if x["tag"] == "markdown" {
				s, _ := x["content"].(string)
				out = append(out, s)
			}
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(v)
	return out
}

type locationsVM struct{}

func (locationsVM) Query(context.Context, string) ([]vm.Sample, error) {
	return []vm.Sample{{Labels: map[string]string{"location": "edge-1" + mention + fakeLink}, Value: 0}}, nil
}

// TestCardsEscapeAlertText: what an alert, a probe's labels or a person's note says goes onto the card as
// text: it mentions nobody and links nowhere.
func TestCardsEscapeAlertText(t *testing.T) {
	e := newEnv(t)
	e.b.d.VM = locationsVM{}
	ctx := context.Background()
	w := hook("firing", "critical", "f1")
	w.CommonAnnotations["description"] = "No answer. " + mention + " " + fakeLink
	w.CommonAnnotations["summary"] = "git is down " + mention
	w.GroupLabels["service"] = "git<at id=all></at>" + fakeLink // no quotes, so its locations are queried
	w.CommonLabels["service"] = w.GroupLabels["service"]
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	id := GroupID(w.GroupKey)
	if _, err := e.b.Ack(ctx, id, Actor{Name: "alice", Via: "api"}, "on it "+mention+" "+fakeLink); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Silence(ctx, id, time.Hour, Actor{Name: "alice", Via: "api"}, "quiet "+mention+" "+fakeLink); err != nil {
		t.Fatal(err)
	}
	g, _ := e.st.Group(id)
	md := markdownOf(t, e.b.card(ctx, &g))
	all := strings.Join(md, "\n")
	for _, want := range []string{"No answer.", "git", "edge-1", "on it"} {
		if !strings.Contains(all, want) {
			t.Errorf("the card lacks %q: %s", want, all)
		}
	}
	for _, bad := range []string{"<at", "](", "[Approve"} {
		if strings.Contains(all, bad) {
			t.Errorf("the card's markdown holds %q: %s", bad, all)
		}
	}

	// the group bot's plain messages and echoes mention nobody either
	g.Status, g.ResolvedAt = "resolved", e.now
	for _, text := range append([]string{e.b.plainFiring(&g), e.b.plainResolved(&g), e.b.plainBody(&g)}, e.bot.texts...) {
		if strings.Contains(text, "<at") {
			t.Errorf("a plain message mentions: %s", text)
		}
	}
	if len(e.bot.texts) < 2 {
		t.Fatalf("echoes: %q", e.bot.texts)
	}
}

// TestNotesAndAgentsOnCardsEscape: the card's other blocks escape what they quote too.
func TestNotesAndAgentsOnCardsEscape(t *testing.T) {
	e := newEnv(t)
	g := &store.Group{ID: "g1", Status: "firing", Severity: "critical", StartsAt: e.now,
		Labels: map[string]string{"alertname": "ServiceDown", "service": "git"},
		Agents: []store.AgentCall{{Hook: "hook" + mention, Event: "firing", Status: "failed"}},
		Notes:  []store.Note{{By: "alice" + mention, Via: "api", At: e.now, Text: fakeLink}}}
	all := strings.Join(markdownOf(t, e.b.card(context.Background(), g)), "\n")
	for _, bad := range []string{"<at", "](", "[Approve"} {
		if strings.Contains(all, bad) {
			t.Errorf("the card's markdown holds %q: %s", bad, all)
		}
	}
}
