package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/notify"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

const cancellationTestToken = "fixturePushoverApplication"

type pushoverFixture struct {
	mu          sync.Mutex
	sent        int
	cancelled   []string
	reject      bool
	firstCancel func()
}

func (f *pushoverFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, `{"status":1,"acknowledged":0}`)
		return
	}
	if err := r.ParseForm(); err != nil || r.Method != http.MethodPost ||
		r.PostForm.Get("token") != cancellationTestToken || r.URL.RawQuery != "" {
		t.Error("Pushover POST did not carry the application token only in the form")
		http.Error(w, "invalid fixture request", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/1/messages.json" {
		f.sent++
		_, _ = fmt.Fprintf(w, `{"status":1,"receipt":"Receipt%03d"}`, f.sent)
		return
	}
	receipt, ok := strings.CutPrefix(r.URL.Path, "/1/receipts/")
	receipt, cancel := strings.CutSuffix(receipt, "/cancel.json")
	if !ok || !cancel || strings.Contains(receipt, "/") || r.PostForm.Has("tags") {
		t.Errorf("wrong exact cancellation path: %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	f.cancelled = append(f.cancelled, receipt)
	if len(f.cancelled) == 1 && f.firstCancel != nil {
		f.firstCancel()
	}
	if f.reject {
		// A provider response without acceptance still has an HTTP success status.
		_, _ = fmt.Fprintf(w, `{"status":0,"errors":["%s %s"]}`, cancellationTestToken, receipt)
		return
	}
	_, _ = io.WriteString(w, `{"status":1}`)
}

func (f *pushoverFixture) allowCancellation() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reject = false
}

func (f *pushoverFixture) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cancelled)
}

func pushoverFixtureDeps(t *testing.T, deps Deps, fixture *pushoverFixture, expire time.Duration) Deps {
	t.Helper()
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.serve(t, w, r)
	}))
	t.Cleanup(sink.Close)
	conf := *deps.Config
	conf.Pushover.API, conf.Pushover.Expire = sink.URL, config.Duration(expire)
	conf.Secrets.PushoverToken, conf.Secrets.PushoverUserKey = cancellationTestToken, "fixtureRecipient"
	conf.Escalation = map[string]config.Policy{
		"critical": {InitialPushover: true, Pushover: config.Duration(time.Second)},
	}
	deps.Config = &conf
	deps.Pushover = &notify.Pushover{API: sink.URL, Token: cancellationTestToken, User: conf.Secrets.PushoverUserKey,
		Retry: time.Minute, Expire: expire}
	return deps
}

func TestPushoverCancellationSurvivesFailureAndReload(t *testing.T) {
	for _, action := range []string{"ack", "resolve"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
			id := GroupID(firing.GroupKey)
			fixture := &pushoverFixture{reject: true, firstCancel: func() {
				g, ok := e.st.Group(id)
				if !ok || len(g.PushoverCancellations) != 1 ||
					action == "ack" && !g.Ack.StopsEscalation() || action == "resolve" && g.Firing() {
					t.Error("cancellation reached the provider before Beacon persisted the action and receipt")
				}
			}}
			deps := pushoverFixtureDeps(t, e.b.d, fixture, time.Hour)
			b := New(deps)
			if err := b.HandleWebhook(ctx, firing); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "ack":
				if _, err := b.Ack(ctx, id, Actor{Name: "Alex", Via: "console", Key: "user:alex"}, "Investigating"); err != nil {
					t.Fatal(err)
				}
			case "resolve":
				if err := b.HandleWebhook(ctx, resolved); err != nil {
					t.Fatal(err)
				}
			}
			firstDelivery := b.LatestCriticalDeliveries()["pushover"]
			if firstDelivery.Result != "ok" || firstDelivery.Destination != DeliveryCurrent {
				t.Fatal("the rejected cancellation replaced the emergency's accepted delivery status")
			}
			view, err := b.Group(id)
			if err != nil || view.PushoverCancellation != "pending" || view.Pushed == nil ||
				!slices.Equal(fixture.calls(), []string{"Receipt001"}) {
				t.Fatalf("the rejected cancellation was not pending: %#v, %v, %v", view, fixture.calls(), err)
			}
			if err := b.HandleWebhook(ctx, resolved); err != nil {
				t.Fatal(err)
			}
			// Reload with the same persisted state. Duplicate resolution and early ticks send nothing.
			b = New(deps)
			e.now = e.now.Add(notificationRetry - time.Second)
			b.Tick(ctx)
			if len(fixture.calls()) != 1 {
				t.Fatal("cancellation was repeated before its backoff")
			}
			fixture.allowCancellation()
			e.now = e.now.Add(time.Second)
			b.Tick(ctx)
			b.Tick(ctx)
			view, err = b.Group(id)
			if err != nil || view.PushoverCancellation != "confirmed" ||
				!slices.Equal(fixture.calls(), []string{"Receipt001", "Receipt001"}) {
				t.Fatalf("the retry was not accepted exactly once: status=%s calls=%v err=%v", view.PushoverCancellation, fixture.calls(), err)
			}
			timeline, err := b.Timeline(ctx, id, "")
			if err != nil {
				t.Fatal(err)
			}
			var results []string
			for _, event := range timeline.Events {
				if event.Kind == "notification" && event.Event == "cancel" && event.Via == "pushover" {
					results = append(results, event.Result)
				}
			}
			if !slices.Equal(results, []string{"failed", "ok"}) {
				t.Fatalf("incident timeline omitted the cancellation outcome: %v", results)
			}
			output, err := json.Marshal(struct {
				Group    GroupView
				Timeline Timeline
				Audit    any
			}{view, timeline, e.st.AuditLog(10)})
			if err != nil || strings.Contains(string(output), cancellationTestToken) || strings.Contains(string(output), "Receipt001") {
				t.Fatal("the cancellation exposed provider identifiers")
			}
			if got := b.LatestCriticalDeliveries()["pushover"]; got != firstDelivery {
				t.Fatal("accepting a cancellation altered the initial notification evidence")
			}
			if !strings.Contains(string(output), "pushover_cancel") {
				t.Fatal("the provider outcome had no audit entry")
			}
		})
	}
}

func TestPushoverCancellationStaysBoundWhenASecondEmergencyIsSent(t *testing.T) {
	for _, next := range []string{"new episode", "after acknowledgement lapses"} {
		t.Run(next, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			holder := &pushoverFixture{reject: true}
			deps := pushoverFixtureDeps(t, e.b.d, holder, 2*time.Hour)
			deps.Config.AckTTL = config.Duration(15 * time.Second)
			b := New(deps)
			firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
			id := GroupID(firing.GroupKey)
			if err := b.HandleWebhook(ctx, firing); err != nil {
				t.Fatal(err)
			}
			first, _ := e.st.Group(id)
			switch next {
			case "new episode":
				if err := b.HandleWebhook(ctx, resolved); err != nil {
					t.Fatal(err)
				}
				if err := b.HandleWebhook(ctx, firing); err != nil {
					t.Fatal(err)
				}
			case "after acknowledgement lapses":
				if _, err := b.Ack(ctx, id, Actor{Name: "Alex", Via: "console", Key: "user:alex"}, "Investigating"); err != nil {
					t.Fatal(err)
				}
				e.now = e.now.Add(16 * time.Second)
				b.Tick(ctx)
				e.now = e.now.Add(time.Second)
				b.Tick(ctx)
			}
			group, _ := e.st.Group(id)
			if group.Receipt != "Receipt002" || group.PushoverCancelStatus != "" ||
				!slices.Equal(holder.calls(), []string{"Receipt001"}) {
				t.Fatal("the second emergency inherited the original cancellation")
			}

			displaced := &pushoverFixture{}
			changed := pushoverFixtureDeps(t, deps, displaced, 2*time.Hour)
			e.now = e.now.Add(notificationRetry)
			New(changed).Tick(ctx)
			if len(displaced.calls()) != 0 || len(holder.calls()) != 1 {
				t.Fatal("a replacement provider was given the previous emergency receipt")
			}
			holder.allowCancellation()
			b = New(deps)
			b.Tick(ctx)
			group, _ = e.st.Group(id)
			if group.Receipt != "Receipt002" || group.PushoverCancelStatus != "" ||
				!slices.Equal(holder.calls(), []string{"Receipt001", "Receipt001"}) {
				t.Fatal("retry of the first receipt touched the later page")
			}
			audit := e.st.AuditLog(10)
			target := id
			if first.Episode != group.Episode {
				target = link.IncidentPath(id, first.Episode)
			}
			if !slices.ContainsFunc(audit, func(entry store.AuditEntry) bool {
				return entry.Action == "pushover_cancel" && entry.Target == target && strings.HasPrefix(entry.Detail, "ok")
			}) {
				t.Fatalf("the previous emergency was not audited against the originating incident: %v", audit)
			}
			if target != id && slices.ContainsFunc(audit, func(entry store.AuditEntry) bool {
				return entry.Action == "pushover_cancel" && entry.Target == id && strings.HasPrefix(entry.Detail, "ok")
			}) {
				t.Fatal("the prior episode's cancellation was included in the current group audit")
			}
			if err := b.HandleWebhook(ctx, resolved); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(holder.calls(), []string{"Receipt001", "Receipt001", "Receipt002"}) {
				t.Fatalf("recovery did not cancel the exact later receipt: %v", holder.calls())
			}
		})
	}
}

func TestPushoverCancellationExpiresAtOriginalProviderDeadline(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fixture := &pushoverFixture{reject: true}
	deps := pushoverFixtureDeps(t, e.b.d, fixture, 40*time.Second)
	b := New(deps)
	firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
	if err := b.HandleWebhook(ctx, firing); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleWebhook(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	deps.Config.Pushover.Expire = config.Duration(2 * time.Hour)
	fixture.allowCancellation()
	e.now = e.now.Add(notificationRetry)
	b = New(deps)
	b.Tick(ctx)
	g, _ := e.st.Group(GroupID(firing.GroupKey))
	if len(fixture.calls()) != 1 || len(g.PushoverCancellations) != 0 || g.PushoverCancelStatus != "expired" {
		t.Fatalf("cancellation outlived the original page: calls=%v pending=%d status=%s",
			fixture.calls(), len(g.PushoverCancellations), g.PushoverCancelStatus)
	}
}

func TestPushoverCancellationNeverGuessesAnOlderRoute(t *testing.T) {
	for _, verified := range []bool{true, false} {
		t.Run(fmt.Sprint(verified), func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			fixture := &pushoverFixture{}
			deps := pushoverFixtureDeps(t, e.b.d, fixture, time.Hour)
			b := New(deps)
			firing, resolved := hook("firing", "critical", "f1"), hook("resolved", "critical", "f1")
			if err := b.HandleWebhook(ctx, firing); err != nil {
				t.Fatal(err)
			}
			g, _ := e.st.Group(GroupID(firing.GroupKey))
			g.ReceiptRouteID, g.ReceiptExpiresAt = "", time.Time{}
			if !verified {
				g.Notifications = nil
			}
			if err := e.st.PutGroup(g); err != nil {
				t.Fatal(err)
			}
			// A new shorter expiry cannot establish when a legacy receipt stopped repeating.
			deps.Config.Pushover.Expire = config.Duration(40 * time.Second)
			e.now = e.now.Add(time.Minute)
			b = New(deps)
			if err := b.HandleWebhook(ctx, resolved); err != nil {
				t.Fatal(err)
			}
			if (len(fixture.calls()) == 1) != verified {
				t.Fatalf("legacy cancellation attempted without matching stored acceptance: %v", fixture.calls())
			}
			want := "unconfirmed"
			if verified {
				want = "confirmed"
			}
			if v, err := b.Group(g.ID); err != nil || v.PushoverCancellation != want {
				t.Fatalf("legacy receipt state is %q, want %q: %v", v.PushoverCancellation, want, err)
			}
		})
	}
}
