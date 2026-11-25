package bridge

import (
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

const (
	notificationRetry = time.Minute
	keepNotifications = 24
)

func latestNotification(g *store.Group, channel string) store.Notification {
	for i := len(g.Notifications) - 1; i >= 0; i-- {
		if g.Notifications[i].Channel == channel {
			return g.Notifications[i]
		}
	}
	return store.Notification{}
}

func (b *Bridge) recordNotification(g *store.Group, channel string, err error) {
	now := b.now()
	result := "ok"
	if err != nil {
		result = "failed"
	}
	prev := latestNotification(g, channel)
	chatAccepted := result == "ok" && (channel == "app" || channel == "webhook")
	if !chatAccepted && (prev.Result != result || prev.At.Before(g.NotifiedAt)) {
		_ = b.d.Store.Audit(store.AuditEntry{Time: now, Actor: "beacon", Via: channel, Action: "notify_people", Target: g.ID, Detail: result})
	}
	g.Notifications = append(g.Notifications, store.Notification{At: now, Channel: channel, Result: result})
	if len(g.Notifications) > keepNotifications {
		g.Notifications = g.Notifications[len(g.Notifications)-keepNotifications:]
	}
}

func notificationDue(g *store.Group, channel string, now time.Time) bool {
	last := latestNotification(g, channel)
	return last.At.Before(g.NotifiedAt) || now.Sub(last.At) >= notificationRetry
}

// Older versions also set Pushed after a failed request. A receipt or a matching receipt check
// distinguishes API acceptance in those records, including after the receipt expires.
func pushoverAccepted(g *store.Group) bool {
	return !g.Pushed.IsZero() && (g.Receipt != "" || !g.ReceiptAt.Before(g.Pushed))
}

func acceptedPushoverAt(g *store.Group) *time.Time {
	if pushoverAccepted(g) {
		return tp(g.Pushed)
	}
	return nil
}

func notificationEvents(g *store.Group) []Event {
	var events []Event
	if (g.Channel == "app" || g.Channel == "webhook") && latestNotification(g, g.Channel).Result != "ok" {
		at := g.ChannelAt
		if at.IsZero() {
			at = g.NotifiedAt
		}
		events = append(events, Event{At: at, Kind: "notified", Via: g.Channel, Result: "ok"})
	}
	seen := map[string]bool{}
	for _, n := range g.Notifications {
		kind, via := "notification", n.Channel
		if n.Result == "ok" {
			switch n.Channel {
			case "app", "webhook":
				kind = "notified"
			case "feishu-buzz":
				kind, via = "buzzed", "feishu"
			case "pushover":
				kind = "pushover"
			}
		}
		events = append(events, Event{At: n.At, Kind: kind, Via: via, Result: n.Result})
		seen[n.Channel] = true
	}
	if !seen["feishu-buzz"] {
		events = append(events, Event{At: g.Buzzed, Kind: "notification", Via: "feishu-buzz"})
	}
	if !seen["pushover"] {
		if pushoverAccepted(g) {
			events = append(events, Event{At: g.Pushed, Kind: "pushover", Via: "pushover", Result: "ok"})
		} else {
			events = append(events, Event{At: g.Pushed, Kind: "notification", Via: "pushover"})
		}
	}
	if !seen["bark"] {
		events = append(events, Event{At: g.Barked, Kind: "notification", Via: "bark"})
	}
	return events
}

// DeliveryAttempt links a recent provider response to its incident.
type DeliveryAttempt struct {
	AlertID, Result string
	At              time.Time
}

// LatestCriticalDeliveries returns the newest recorded outcome per channel among retained critical incidents.
func (b *Bridge) LatestCriticalDeliveries() map[string]DeliveryAttempt {
	results := map[string]DeliveryAttempt{}
	for _, g := range b.d.Store.Groups() {
		if g.Severity != "critical" {
			continue
		}
		for _, e := range notificationEvents(&g) {
			channel := e.Via
			if e.Kind == "buzzed" {
				channel = "feishu-buzz"
			}
			if (e.Result == "ok" || e.Result == "failed") && e.At.After(results[channel].At) {
				results[channel] = DeliveryAttempt{AlertID: g.ID, Result: e.Result, At: e.At}
			}
		}
	}
	return results
}
