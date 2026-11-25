package bridge

import (
	"context"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

const maxPushoverLifetime = 3 * time.Hour

func pushoverLifetime(expire time.Duration) time.Duration {
	if expire <= 0 || expire > maxPushoverLifetime {
		return maxPushoverLifetime
	}
	return expire.Truncate(time.Second)
}

func pushoverReceiptRoute(g *store.Group) string {
	if g.ReceiptRouteID != "" {
		return g.ReceiptRouteID
	}
	// Earlier releases recorded the provider destination on the accepted page.
	for _, n := range slices.Backward(g.Notifications) {
		if n.Channel == "pushover" && n.Event == "" && n.Result == "ok" && !n.At.After(g.Pushed) {
			return n.RouteID
		}
	}
	return ""
}

func (b *Bridge) pushoverReceiptRouteMatches(g *store.Group) bool {
	route := pushoverReceiptRoute(g)
	return route == "" || route == b.deliveryRoutes["pushover"]
}

func (b *Bridge) queuePushoverCancellation(g *store.Group) {
	if !pushoverAccepted(g) || g.Receipt == "" || g.PushoverCancelStatus != "" {
		return
	}
	now := b.now()
	b.expirePushoverCancellations(g, now)
	expires := g.ReceiptExpiresAt
	if expires.IsZero() {
		expires = g.Pushed.Add(maxPushoverLifetime)
	}
	if !expires.After(now) {
		g.PushoverCancelStatus = "expired"
		return
	}
	route := pushoverReceiptRoute(g)
	if route == "" {
		g.PushoverCancelStatus = "unconfirmed"
		b.d.Log.Warn("cannot verify the Pushover destination for cancellation", "group", g.ID, "episode", g.Episode)
		return
	}
	if len(g.PushoverCancellations) >= keepNotifications {
		g.PushoverCancellations = g.PushoverCancellations[1:]
		b.d.Log.Warn("discarded oldest pending Pushover cancellation", "group", g.ID)
	}
	id := newDelivery()
	g.PushoverCancelID, g.PushoverCancelStatus = id, "pending"
	g.PushoverCancellations = append(g.PushoverCancellations, store.PushoverCancellation{
		ID: id, Episode: g.Episode, Receipt: g.Receipt, RouteID: route, ExpiresAt: expires,
	})
}

func (b *Bridge) expirePushoverCancellations(g *store.Group, now time.Time) bool {
	before := len(g.PushoverCancellations)
	g.PushoverCancellations = slices.DeleteFunc(g.PushoverCancellations, func(n store.PushoverCancellation) bool {
		if n.ExpiresAt.After(now) {
			return false
		}
		if n.ID == g.PushoverCancelID {
			g.PushoverCancelStatus = "expired"
		}
		return true
	})
	return len(g.PushoverCancellations) != before
}

// submitNewPushoverCancellation keeps the first attempt prompt after the caller saves the intent.
// Repeated actions reuse the same intent and leave retries to leader upkeep.
func (b *Bridge) submitNewPushoverCancellation(ctx context.Context, g *store.Group) {
	if n := len(g.PushoverCancellations); n > 0 {
		last := g.PushoverCancellations[n-1]
		if last.ID == g.PushoverCancelID && last.RetryAt.IsZero() {
			b.sendPushoverCancellation(ctx, g, n-1, b.now())
		}
	}
}

// sendPushoverCancellation requires a persisted receipt and route. The caller holds mu.
func (b *Bridge) sendPushoverCancellation(ctx context.Context, g *store.Group, index int, now time.Time) bool {
	n := g.PushoverCancellations[index]
	if ctx.Err() != nil || b.d.Pushover == nil || n.RouteID == "" || n.RouteID != b.deliveryRoutes["pushover"] ||
		n.RetryAt.After(now) || !n.ExpiresAt.After(now) {
		return false
	}
	// Persist the next eligible time before the call. A restart after an ambiguous result
	// must keep the backoff and retry only this receipt.
	g.PushoverCancellations[index].RetryAt = now.Add(notificationRetry)
	if err := b.d.Store.PutGroup(*g); err != nil {
		g.PushoverCancellations[index].RetryAt = n.RetryAt
		b.d.Log.Error("schedule Pushover cancellation", "group", g.ID, "episode", n.Episode, "err", err)
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, min(5*time.Second, n.ExpiresAt.Sub(now)))
	defer cancel()

	result := "ok"
	err := b.d.Pushover.Cancel(ctx, n.Receipt)
	if err == nil {
		g.PushoverCancellations = slices.Delete(g.PushoverCancellations, index, index+1)
		if n.ID == g.PushoverCancelID {
			g.PushoverCancelStatus = "confirmed"
		}
		b.count("pushover-cancel", "ok")
	} else {
		result = "failed"
		b.count("pushover-cancel", "error")
		b.d.Log.Warn("Pushover cancellation submission failed", "group", g.ID, "episode", n.Episode)
	}
	if err == nil || n.RetryAt.IsZero() {
		if n.Episode == g.Episode {
			appendNotification(g, store.Notification{At: b.now(), Channel: "pushover", Event: "cancel", Result: result, RouteID: n.RouteID})
		}
		target := g.ID
		if n.Episode != g.Episode {
			// The current episode's related audit shows group-targeted entries after it began.
			target = link.IncidentPath(g.ID, n.Episode)
		}
		entry := store.AuditEntry{Time: b.now(), Actor: "beacon", Via: "pushover", Action: "pushover_cancel", Target: target, Detail: result}
		if n.Episode != "" {
			entry.Detail += " episode " + n.Episode
		}
		if err := b.d.Store.PutGroupAndAudit(*g, nil, entry); err != nil {
			b.d.Log.Error("save Pushover cancellation", "group", g.ID, "episode", n.Episode, "err", err)
		}
	}
	return true
}

func (b *Bridge) retryPushoverCancellation(ctx context.Context, groups []store.Group, now time.Time) {
	if len(groups) == 0 {
		return
	}
	start := b.pushoverCancelCursor % len(groups)
	for offset := range len(groups) {
		if ctx.Err() != nil {
			return
		}
		index := (start + offset) % len(groups)
		g := &groups[index]
		if !g.Firing() && now.Sub(g.ResolvedAt) > keepResolved {
			continue // already archived by this tick
		}
		expired := b.expirePushoverCancellations(g, now)
		for j := range g.PushoverCancellations {
			if b.sendPushoverCancellation(ctx, g, j, now) {
				b.pushoverCancelCursor = (index + 1) % len(groups)
				return
			}
		}
		if expired {
			if err := b.d.Store.PutGroup(*g); err != nil {
				b.d.Log.Error("save expired Pushover cancellations", "group", g.ID, "err", err)
			}
		}
	}
}
