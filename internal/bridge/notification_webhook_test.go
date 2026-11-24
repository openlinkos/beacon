package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
)

type webhookSendFunc func(context.Context, []byte) error

func (f webhookSendFunc) Send(ctx context.Context, body []byte) error { return f(ctx, body) }

func runWebhookDispatch(b *Bridge, ctx context.Context) {
	var work sync.WaitGroup
	b.sendWebhooks(ctx, &work)
	work.Wait()
}

func TestNotificationWebhookRetainsOrderedTransitions(t *testing.T) {
	e := newEnv(t)
	e.b.d.Config.PublicURL = "https://beacon.example.com/"
	e.b.d.Config.Escalation["critical"] = config.Policy{Channel: config.NotificationWebhookChannel}
	var attempts [][]byte
	deps := e.b.d
	deps.NotificationWebhook = webhookSendFunc(func(_ context.Context, body []byte) error {
		attempts = append(attempts, bytes.Clone(body))
		if len(attempts) == 1 {
			return errors.New("receiver unavailable")
		}
		return nil
	})
	b := New(deps)
	ctx := context.Background()
	w := hook("firing", "critical", "first")
	id := GroupID(w.GroupKey)
	if d := b.Delivery(); !d.NotificationWebhook || !d.CriticalToPeople || d.CriticalChat != config.NotificationWebhookChannel ||
		len(d.CriticalRoutes) != 1 || d.CriticalRoutes[0].Initial != "primary" {
		t.Fatalf("delivery setup: %+v", d)
	}
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatal("handle webhook sent the human notification before persisting it")
	}
	runWebhookDispatch(b, ctx)
	g, ok := e.st.Group(id)
	if !ok || len(g.WebhookNotices) != 1 || !g.WebhookNotices[0].RetryAt.Equal(e.now.Add(notificationRetry)) || g.Channel != "none" {
		t.Fatalf("failed delivery: %+v, exists=%v", g.WebhookNotices, ok)
	}
	if ready, _ := b.Ready(); ready {
		t.Fatal("bridge reports ready with a failed delivery waiting to retry")
	}
	w.Status = "resolved"
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	w.Status = "firing"
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	b = New(deps)
	e.now = e.now.Add(notificationRetry - 2*time.Second)
	runWebhookDispatch(b, ctx)
	if len(attempts) != 1 {
		t.Fatal("retention and restart advanced the retry time")
	}
	e.now = e.now.Add(time.Second)
	runWebhookDispatch(b, ctx)
	if len(attempts) != 2 || !bytes.Equal(attempts[0], attempts[1]) {
		t.Fatal("the first delivery's body changed when it was retried")
	}
	runWebhookDispatch(b, ctx)
	g, _ = e.st.Group(id)
	if g.Channel != "none" || len(g.Notifications) != 0 {
		t.Fatal("a response to an older episode changed the current episode's delivery history")
	}
	runWebhookDispatch(b, ctx)
	if len(attempts) != 4 {
		t.Fatalf("expected firing, retry, resolution and second firing, got %d attempts", len(attempts))
	}
	var fired, resolved, firedAgain webhookNotice
	for i, dest := range []*webhookNotice{&fired, &resolved, &firedAgain} {
		if err := json.Unmarshal(attempts[i+1], dest); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if fired.Version != 1 || fired.Event != "firing" || resolved.Event != "resolved" || firedAgain.Event != "firing" ||
		fired.Alert.Episode == "" || fired.Alert.Episode != resolved.Alert.Episode || fired.Alert.Episode == firedAgain.Alert.Episode ||
		fired.Delivery == resolved.Delivery || resolved.Delivery == firedAgain.Delivery {
		t.Fatalf("delivery order or identities: %+v, %+v, %+v", fired, resolved, firedAgain)
	}
	wantURL := "https://beacon.example.com/console/sign-in?next=%2Fconsole%2Falerts%2F" + id
	if fired.Alert.ID != id || fired.Alert.URL != wantURL || fired.Alert.Severity != "critical" {
		t.Fatalf("notification alert: %+v", fired.Alert)
	}
	g, _ = e.st.Group(id)
	latest := latestNotification(&g, config.NotificationWebhookChannel)
	if g.Channel != config.NotificationWebhookChannel || len(g.WebhookNotices) != 0 || latest.Result != "ok" {
		t.Fatalf("delivered notice: channel=%q queued=%d result=%q", g.Channel, len(g.WebhookNotices), latest.Result)
	}
	if ready, why := b.Ready(); !ready {
		t.Fatalf("ready after delivery: %q", why)
	}
}

func TestNotificationWebhookBlockedReceiverLeavesAlertAvailable(t *testing.T) {
	e := newEnv(t)
	e.b.d.Config.Escalation["critical"] = config.Policy{Channel: config.NotificationWebhookChannel}
	deps := e.b.d
	started := make(chan struct{})
	deps.NotificationWebhook = webhookSendFunc(func(ctx context.Context, _ []byte) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	b := New(deps)
	ctx, cancel := context.WithCancel(context.Background())
	var work sync.WaitGroup
	defer func() { cancel(); work.Wait() }()
	w := hook("firing", "critical", "first")
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	b.sendWebhooks(ctx, &work)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("notification never started")
	}
	end := make(chan error, 1)
	go func() {
		w.Status = "resolved"
		end <- b.HandleWebhook(ctx, w)
	}()
	select {
	case err := <-end:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a slow receiver blocked recording the alert's recovery")
	}
	cancel()
	work.Wait()
	g, _ := e.st.Group(GroupID(w.GroupKey))
	if g.Firing() || len(g.WebhookNotices) != 2 || !g.WebhookNotices[0].RetryAt.IsZero() {
		t.Fatalf("interrupted delivery was lost or no longer ready to retry: %+v", g.WebhookNotices)
	}
}

func TestNotificationWebhookExpiresOldQueuedDelivery(t *testing.T) {
	e := newEnv(t)
	e.b.d.Config.Escalation["critical"] = config.Policy{Channel: config.NotificationWebhookChannel}
	deps := e.b.d
	attempted := false
	deps.NotificationWebhook = webhookSendFunc(func(context.Context, []byte) error {
		attempted = true
		return nil
	})
	b := New(deps)
	ctx := context.Background()
	w := hook("firing", "critical", "first")
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(keepResolved)
	runWebhookDispatch(b, ctx)
	id := GroupID(w.GroupKey)
	g, _ := e.st.Group(id)
	if attempted || !g.Firing() || len(g.WebhookNotices) != 0 {
		t.Fatalf("expired delivery kept or attempted: %+v", g.WebhookNotices)
	}
	log := e.st.AuditLog(1)
	if len(log) != 1 || log[0].Via != config.NotificationWebhookChannel || log[0].Target != id || log[0].Detail != "1 queued webhook deliveries expired" {
		t.Fatalf("audit for expired delivery: %+v", log)
	}
}
