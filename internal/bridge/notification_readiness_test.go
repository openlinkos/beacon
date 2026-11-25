package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

func TestDeliveryReadinessMatchesPersistedDestination(t *testing.T) {
	e := newEnv(t)
	const channel = config.NotificationWebhookChannel
	deps := e.b.d
	deps.NotificationWebhook = webhookSendFunc(func(context.Context, []byte) error { return nil })
	oldConfig := *deps.Config
	oldConfig.Secrets.NotificationWebhookURL = "https://notifications.example.com/one"
	oldConfig.Secrets.NotificationWebhookSecret = "isolated receiver secret"
	deps.Config = &oldConfig
	oldBridge := New(deps)
	g := store.Group{ID: GroupID("delivery readiness"), Severity: "critical", Status: "firing", StartsAt: e.now}
	oldBridge.recordNotification(&g, channel, nil)
	if err := deps.Store.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if g.Notifications[0].RouteID == "" || strings.Contains(g.Notifications[0].RouteID, oldConfig.Secrets.NotificationWebhookSecret) {
		t.Fatal("delivery route identity is missing or exposes its secret")
	}
	legacy := store.Group{ID: GroupID("legacy readiness"), Severity: "critical", Status: "firing", StartsAt: e.now,
		Notifications: []store.Notification{{At: e.now.Add(time.Second), Channel: channel, Result: "ok"}}}
	if err := deps.Store.PutGroup(legacy); err != nil {
		t.Fatal(err)
	}
	if a := New(deps).LatestCriticalDeliveries()[channel]; a.Destination != DeliveryCurrent || a.AlertID != g.ID || a.Result != "ok" {
		t.Fatalf("an unchanged restart did not prefer the known matching result: %+v", a)
	}

	newConfig := oldConfig
	newConfig.Secrets.NotificationWebhookURL = "https://notifications.example.com/two"
	deps.Config = &newConfig
	newBridge := New(deps)
	if a := newBridge.LatestCriticalDeliveries()[channel]; a.Destination != DeliveryUnverified || a.AlertID != legacy.ID {
		t.Fatalf("the legacy acceptance was promoted to current: %+v", a)
	}
	if count := deps.Store.DeleteGroups(func(g store.Group) bool { return g.ID == legacy.ID }); count != 1 {
		t.Fatalf("legacy group count deleted = %d", count)
	}
	if a := newBridge.LatestCriticalDeliveries()[channel]; a.Destination != DeliveryPrevious || a.AlertID != g.ID {
		t.Fatalf("the former destination's acceptance was promoted to current: %+v", a)
	}
	e.now = e.now.Add(2 * time.Second)
	newBridge.recordNotification(&g, channel, errors.New("fixture rejected delivery"))
	if err := deps.Store.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if a := New(deps).LatestCriticalDeliveries()[channel]; a.Destination != DeliveryCurrent || a.Result != "failed" {
		t.Fatalf("the current failure did not replace historic acceptance: %+v", a)
	}
	rotatedConfig := newConfig
	rotatedConfig.Secrets.NotificationWebhookSecret = "rotated isolated receiver secret"
	deps.Config = &rotatedConfig
	if a := New(deps).LatestCriticalDeliveries()[channel]; a.Destination != DeliveryPrevious {
		t.Fatalf("a result signed with an old credential was promoted to current: %+v", a)
	}

	timeline, err := newBridge.Timeline(context.Background(), g.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(timeline.Events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), g.Notifications[0].RouteID) || strings.Contains(string(public), newConfig.Secrets.NotificationWebhookURL) {
		t.Fatal("the timeline exposed private notification routing metadata")
	}
}

func TestRecordedChatAcceptanceReplacesLegacyTimelineEvent(t *testing.T) {
	now := time.Now()
	g := store.Group{Channel: "app", ChannelAt: now.Add(-time.Second), Notifications: []store.Notification{
		{Channel: "app", At: now, Result: "ok", RouteID: "fixture route identity"},
	}}
	events := notificationEvents(&g)
	found := 0
	for _, event := range events {
		if event.Via == "app" {
			found++
			if !event.At.Equal(now) || event.Kind != "notified" || event.routeID != g.Notifications[0].RouteID {
				t.Fatalf("the recorded acceptance was altered: %+v", event)
			}
		}
	}
	if found != 1 {
		t.Fatalf("the recorded acceptance appears %d times, want 1", found)
	}
}
