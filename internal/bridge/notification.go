package bridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

const (
	notificationRetry = time.Minute
	keepNotifications = 24
)

func latestNotification(g *store.Group, channel string) store.Notification {
	for i := len(g.Notifications) - 1; i >= 0; i-- {
		if g.Notifications[i].Channel == channel && g.Notifications[i].Event == "" {
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
	appendNotification(g, store.Notification{At: now, Channel: channel, Result: result, RouteID: b.deliveryRoutes[channel]})
}

func appendNotification(g *store.Group, n store.Notification) {
	g.Notifications = append(g.Notifications, n)
	if len(g.Notifications) > keepNotifications {
		g.Notifications = g.Notifications[len(g.Notifications)-keepNotifications:]
	}
}

// notificationRoutes binds a submission to the configuration that actually sent it. The key lives
// in the database, so matching survives a restart without exposing recipients or credentials.
func (b *Bridge) notificationRoutes() map[string]string {
	c := b.d.Config
	sec := c.Secrets
	destinations := make(map[string][]string)
	if b.d.Feishu != nil {
		app := []string{c.Feishu.Domain, sec.FeishuAppID, sec.FeishuAppSecret, c.Feishu.ChatID}
		ids := b.onCallIDs()
		slices.Sort(ids)
		destinations["app"] = app
		destinations["feishu-buzz"] = append(slices.Clone(app), ids...)
	}
	if b.d.Bot != nil {
		destinations["webhook"] = []string{sec.FeishuWebhookURL, sec.FeishuWebhookSecret, c.Feishu.WebhookKeyword}
	}
	if b.d.NotificationWebhook != nil {
		destinations[config.NotificationWebhookChannel] = []string{sec.NotificationWebhookURL, sec.NotificationWebhookSecret}
	}
	if b.d.Slack != nil {
		destinations[config.SlackChannel] = []string{sec.SlackWebhookURL}
	}
	if b.d.Pushover != nil {
		destinations["pushover"] = []string{c.Pushover.API, sec.PushoverUserKey, sec.PushoverToken}
	}
	if b.d.Bark != nil {
		destinations["bark"] = []string{sec.BarkURL}
	}
	if len(destinations) == 0 || b.d.Store == nil {
		return nil
	}
	key, err := b.d.Store.Secret("notification-destinations")
	if err != nil {
		b.d.Log.Warn("notification destinations cannot be verified", "err", err)
		return nil
	}
	defer clear(key)
	routes := make(map[string]string, len(destinations))
	for channel, fields := range destinations {
		body, _ := json.Marshal(append([]string{channel}, fields...))
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(body)
		routes[channel] = hex.EncodeToString(mac.Sum(nil))
	}
	return routes
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

type notificationEvent struct {
	Event
	routeID string
}

func notificationEvents(g *store.Group) []notificationEvent {
	var events []notificationEvent
	chatRecorded := slices.ContainsFunc(g.Notifications, func(n store.Notification) bool {
		return n.Channel == g.Channel && n.Result == "ok" && n.Event == ""
	})
	if (g.Channel == "app" || g.Channel == "webhook") && !chatRecorded {
		at := g.ChannelAt
		if at.IsZero() {
			at = g.NotifiedAt
		}
		events = append(events, notificationEvent{Event: Event{At: at, Kind: "notified", Via: g.Channel, Result: "ok"}})
	}
	seen := map[string]bool{}
	for _, n := range g.Notifications {
		kind, via := "notification", n.Channel
		if n.Result == "ok" && n.Event == "" {
			switch n.Channel {
			case "app", "webhook":
				kind = "notified"
			case "feishu-buzz":
				kind, via = "buzzed", "feishu"
			case "pushover":
				kind = "pushover"
			}
		}
		events = append(events, notificationEvent{Event: Event{At: n.At, Kind: kind, Via: via, Result: n.Result, Event: n.Event}, routeID: n.RouteID})
		if n.Event == "" {
			seen[n.Channel] = true
		}
	}
	if !seen["feishu-buzz"] {
		events = append(events, notificationEvent{Event: Event{At: g.Buzzed, Kind: "notification", Via: "feishu-buzz"}})
	}
	if !seen["pushover"] {
		if pushoverAccepted(g) {
			events = append(events, notificationEvent{Event: Event{At: g.Pushed, Kind: "pushover", Via: "pushover", Result: "ok"}})
		} else {
			events = append(events, notificationEvent{Event: Event{At: g.Pushed, Kind: "notification", Via: "pushover"}})
		}
	}
	if !seen["bark"] {
		events = append(events, notificationEvent{Event: Event{At: g.Barked, Kind: "notification", Via: "bark"}})
	}
	return events
}

// DeliveryAttempt links a recent provider response to its incident.
type DeliveryAttempt struct {
	AlertID, Result string
	At              time.Time
	Destination     DeliveryDestination
}

// DeliveryDestination describes whether a historical outcome matches the current configuration.
type DeliveryDestination int

const (
	DeliveryUnverified DeliveryDestination = iota
	DeliveryPrevious
	DeliveryCurrent
)

// LatestCriticalDeliveries prefers the newest result for each configured destination. If no attempt
// can be matched, the latest historical record still explains why the current route is unverified.
func (b *Bridge) LatestCriticalDeliveries() map[string]DeliveryAttempt {
	results := map[string]DeliveryAttempt{}
	for _, g := range b.d.Store.Groups() {
		if g.Severity != "critical" {
			continue
		}
		for _, e := range notificationEvents(&g) {
			if e.Event.Event != "" || e.At.IsZero() || (e.Result != "ok" && e.Result != "failed") {
				continue
			}
			channel := e.Via
			if e.Kind == "buzzed" {
				channel = "feishu-buzz"
			}
			destination := DeliveryUnverified
			if route := b.deliveryRoutes[channel]; e.routeID != "" && route != "" {
				destination = DeliveryPrevious
				if e.routeID == route {
					destination = DeliveryCurrent
				}
			}
			prior := results[channel]
			current, priorCurrent := destination == DeliveryCurrent, prior.Destination == DeliveryCurrent
			if (current && !priorCurrent) || (current == priorCurrent && e.At.After(prior.At)) {
				results[channel] = DeliveryAttempt{AlertID: g.ID, Result: e.Result, At: e.At, Destination: destination}
			}
		}
	}
	return results
}
