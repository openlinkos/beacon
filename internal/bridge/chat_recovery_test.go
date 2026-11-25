package bridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/notify"
)

type recoveryApp struct {
	*fakeFeishu
	messageIDs  []string
	cards       []feishu.Card
	reject      bool
	beforePatch func()
}

func (a *recoveryApp) PatchCard(ctx context.Context, id string, card feishu.Card) error {
	a.messageIDs = append(a.messageIDs, id)
	a.cards = append(a.cards, card)
	if a.beforePatch != nil {
		a.beforePatch()
	}
	if a.reject {
		a.count()
		return errors.New("provider rejected the recovery")
	}
	return a.fakeFeishu.PatchCard(ctx, id, card)
}

func TestFeishuAppRecoveryRetriesAcrossRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := &recoveryApp{fakeFeishu: e.fs, reject: true}
	deps := e.b.d
	deps.Feishu = app
	deps.Pushover = nil
	deps.Config.PublicURL = "https://beacon.example.com"
	b := New(deps)

	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	id := GroupID(firing.GroupKey)
	app.beforePatch = func() {
		g, ok := e.st.Group(id)
		if !ok || g.Firing() || len(g.ChatRecoveries) != 1 {
			t.Fatal("the provider was called before the recovery was saved")
		}
	}
	if err := b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	g, _ := e.st.Group(id)
	episode := g.Episode
	if g.Firing() || len(g.ChatRecoveries) != 1 || !slices.Equal(app.messageIDs, []string{"om_1"}) {
		t.Fatalf("queue after the first rejection: group=%s recovery=%d patches=%v", g.Status, len(g.ChatRecoveries), app.messageIDs)
	}
	if err := b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	restarted := New(deps)
	e.now = e.now.Add(notificationRetry - time.Second)
	restarted.Tick(ctx)
	if len(app.messageIDs) != 1 {
		t.Fatal("duplicate or early retry reached the app")
	}
	if _, err := restarted.AddNote(ctx, id, Actor{Name: "Alex", Via: "api"}, "Checked service logs after recovery."); err != nil {
		t.Fatal(err)
	}
	// The pending recovery owns resolved-card updates, and includes a note added meanwhile.
	if len(app.messageIDs) != 1 {
		t.Fatal("a note bypassed recovery backoff")
	}
	e.now = e.now.Add(time.Second)
	app.reject = false
	restarted.Tick(ctx)
	g, _ = e.st.Group(id)
	if len(g.ChatRecoveries) != 0 || !slices.Equal(app.messageIDs, []string{"om_1", "om_1"}) {
		t.Fatalf("the accepted retry did not clear the queue: pending=%d patches=%v", len(g.ChatRecoveries), app.messageIDs)
	}
	for _, want := range []string{`"template":"green"`, "Checked service logs after recovery.", "%2Fepisodes%2F" + episode} {
		if text := cardText(app.cards[1]); !strings.Contains(text, want) {
			t.Errorf("the recovered card lacks %q: %s", want, text)
		}
	}
	timeline, err := restarted.Timeline(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	var results []string
	for _, event := range timeline.Events {
		if event.Kind == "notification" && event.Event == "resolved" && event.Via == "app" {
			results = append(results, event.Result)
		}
	}
	if !slices.Equal(results, []string{"failed", "ok"}) {
		t.Fatalf("recovery outcomes in the incident timeline: %v", results)
	}
	e.now = e.now.Add(notificationRetry)
	restarted.Tick(ctx)
	if len(app.messageIDs) != 2 {
		t.Fatal("resent an accepted recovery")
	}
}

func TestFeishuRecoveryUsesOriginalMessageAfterRefiring(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := &recoveryApp{fakeFeishu: e.fs, reject: true}
	deps := e.b.d
	deps.Feishu, deps.Pushover = app, nil
	deps.Config.PublicURL = "https://beacon.example.com"
	b := New(deps)
	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	id := GroupID(firing.GroupKey)
	if err := b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	before, _ := e.st.Group(id)
	if before.MessageID != "om_2" || len(before.ChatRecoveries) != 1 || before.ChatRecoveries[0].Episode == before.Episode {
		t.Fatalf("the next episode did not preserve the original pending recovery: %+v", before)
	}
	e.now = e.now.Add(notificationRetry)
	app.reject = false
	b.Tick(ctx)
	after, _ := e.st.Group(id)
	if !after.Firing() || after.MessageID != "om_2" || after.CardHash != before.CardHash || len(after.ChatRecoveries) != 0 ||
		!slices.Equal(app.messageIDs, []string{"om_1", "om_1"}) ||
		!slices.Equal(after.Notifications, before.Notifications) {
		t.Fatalf("the original PATCH affected the replacement episode: message=%s patches=%v", after.MessageID, app.messageIDs)
	}
	if got := cardText(app.cards[1]); !strings.Contains(got, "%2Fepisodes%2F"+before.ChatRecoveries[0].Episode) ||
		strings.Contains(got, "%2Fepisodes%2F"+after.Episode) {
		t.Fatalf("retried card was not tied to the original episode: %s", got)
	}
}

func TestFeishuGroupBotRecoveryRetriesOnlyOriginalDestination(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	deps := e.b.d
	var original, other atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch original.Add(1) {
		case 1:
			if !strings.Contains(string(body), "Firing") {
				t.Errorf("unexpected firing payload: %s", body)
			}
		case 2:
			_, _ = io.WriteString(w, `{"StatusCode":19021}`)
			return
		case 3:
			if !strings.Contains(string(body), "Resolved: ") || !strings.Contains(string(body), "%2Fepisodes%2F") {
				t.Errorf("unexpected recovery payload: %s", body)
			}
		}
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer sink.Close()
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		other.Add(1)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer replacement.Close()

	e.fs.fail = true // Have the firing card fall back to the group bot.
	deps.Pushover = nil
	deps.Bot = &notify.GroupBot{URL: sink.URL}
	deps.Config.Secrets.FeishuWebhookURL = sink.URL
	deps.Config.PublicURL = "https://beacon.example.com"
	b := New(deps)
	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	id := GroupID(firing.GroupKey)
	if err := b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := b.HandleWebhook(ctx, resolved); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := e.st.Group(id)
	if g.Firing() || original.Load() != 2 || len(g.ChatRecoveries) != 1 {
		t.Fatalf("bot recovery was not retained after rejection: state=%s calls=%d pending=%d", g.Status, original.Load(), len(g.ChatRecoveries))
	}

	changed := *deps.Config
	changed.Secrets.FeishuWebhookURL = replacement.URL
	changedDeps := deps
	changedDeps.Config = &changed
	changedDeps.Bot = &notify.GroupBot{URL: replacement.URL}
	e.now = e.now.Add(notificationRetry)
	New(changedDeps).Tick(ctx)
	if other.Load() != 0 || original.Load() != 2 {
		t.Fatalf("retry retargeted the queued recovery: first=%d second=%d", original.Load(), other.Load())
	}

	restarted := New(deps)
	restarted.Tick(ctx)
	restarted.Tick(ctx)
	g, _ = e.st.Group(id)
	if original.Load() != 3 || other.Load() != 0 || len(g.ChatRecoveries) != 0 {
		t.Fatalf("retry did not complete in the original chat: first=%d second=%d pending=%d", original.Load(), other.Load(), len(g.ChatRecoveries))
	}
}

func TestFeishuRecoveryKeepsMonthlyBudget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	deps := e.b.d
	deps.Config.Feishu.MonthlyBudget = 1
	b := New(deps)
	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	id := GroupID(firing.GroupKey)
	if err := b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	g, _ := e.st.Group(id)
	if len(e.fs.patches) != 0 || len(g.ChatRecoveries) != 1 {
		t.Fatalf("recovery exceeded the monthly budget or was lost: patches=%d pending=%d", len(e.fs.patches), len(g.ChatRecoveries))
	}
	// The next month starts within the existing retention window.
	e.now = e.now.Add(3 * 24 * time.Hour)
	b.Tick(ctx)
	g, _ = e.st.Group(id)
	if len(e.fs.patches) != 1 || len(g.ChatRecoveries) != 0 {
		t.Fatalf("recovery did not use the new monthly budget: patches=%d pending=%d", len(e.fs.patches), len(g.ChatRecoveries))
	}
}
