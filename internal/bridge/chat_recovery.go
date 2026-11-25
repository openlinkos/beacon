package bridge

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/store"
)

// chatRoute also recognises firing attempts saved before the destination had a dedicated field.
func chatRoute(g *store.Group) string {
	if g.ChatRouteID != "" {
		return g.ChatRouteID
	}
	for _, n := range slices.Backward(g.Notifications) {
		if n.Channel == g.Channel && n.Result == "ok" && n.Event == "" {
			return n.RouteID
		}
	}
	return ""
}

func currentChatRecovery(g *store.Group, notice store.ChatRecovery) bool {
	return !g.Firing() && g.Episode == notice.Episode && g.ResolvedAt.Equal(notice.At) &&
		g.Channel == notice.Channel && g.MessageID == notice.MessageID
}

func hasCurrentChatRecovery(g *store.Group) bool {
	return !g.Firing() && slices.ContainsFunc(g.ChatRecoveries, func(n store.ChatRecovery) bool {
		return currentChatRecovery(g, n)
	})
}

func (b *Bridge) queueChatRecovery(ctx context.Context, g *store.Group) bool {
	if g.Channel != "app" && g.Channel != "webhook" {
		return false
	}
	route := chatRoute(g)
	if route == "" {
		if g.Channel == "webhook" {
			b.d.Log.Warn("cannot verify the group bot destination for recovery", "group", g.ID, "episode", g.Episode)
		}
		return false
	}
	n := store.ChatRecovery{At: g.ResolvedAt, Episode: g.Episode, Channel: g.Channel, RouteID: route, MessageID: g.MessageID}
	if g.Channel == "app" {
		if g.MessageID == "" {
			return false
		}
		card, err := json.Marshal(b.card(ctx, g))
		if err != nil {
			b.d.Log.Warn("cannot prepare the Feishu recovery card", "group", g.ID, "episode", g.Episode)
			return false
		}
		n.Card = card
	} else {
		n.Text = b.plainResolved(g)
	}
	g.ChatRecoveries = slices.DeleteFunc(g.ChatRecoveries, func(n store.ChatRecovery) bool {
		return !n.At.Add(keepResolved).After(g.ResolvedAt)
	})
	if len(g.ChatRecoveries) >= keepNotifications {
		g.ChatRecoveries = g.ChatRecoveries[1:]
		b.d.Log.Warn("discarded oldest undelivered Feishu recovery", "group", g.ID)
	}
	g.ChatRecoveries = append(g.ChatRecoveries, n)
	return true
}

func (b *Bridge) chatRecoveryDue(n store.ChatRecovery, now time.Time) bool {
	if n.RouteID == "" || b.deliveryRoutes[n.Channel] != n.RouteID || n.RetryAt.After(now) || !n.At.Add(keepResolved).After(now) {
		return false
	}
	switch n.Channel {
	case "app":
		return b.d.Feishu != nil && n.MessageID != "" && b.quota() < 1
	case "webhook":
		return b.d.Bot != nil
	default:
		return false
	}
}

// sendChatRecovery uses only the original destination. A replacement episode never inherits its
// predecessor's card, payload or provider outcome. The caller holds mu and has persisted the queue.
func (b *Bridge) sendChatRecovery(ctx context.Context, g *store.Group, index int, now time.Time) bool {
	n := g.ChatRecoveries[index]
	if ctx.Err() != nil || !b.chatRecoveryDue(n, now) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	current := currentChatRecovery(g, n)
	var err error
	switch n.Channel {
	case "app":
		var card feishu.Card
		var sessions cardSessions
		if current {
			sessions = b.sessionSummary(g)
			card = b.cardWithSessions(ctx, g, sessions)
		} else if err = json.Unmarshal(n.Card, &card); err != nil {
			b.d.Log.Error("invalid saved Feishu recovery card", "group", g.ID, "episode", n.Episode)
			break
		}
		err = b.d.Feishu.PatchCard(ctx, n.MessageID, card)
		b.feishuResult(err)
		if err == nil {
			b.count("feishu-patch", "ok")
		} else {
			b.count("feishu-patch", "error")
		}
		if err == nil && current {
			g.CardAt, g.CardHash, g.CardSessionHash = b.now(), cardHash(g), sessions.fingerprint()
		}
	case "webhook":
		err = b.d.Bot.Send(ctx, n.Text)
	}
	result := "ok"
	if err == nil {
		g.ChatRecoveries = slices.Delete(g.ChatRecoveries, index, index+1)
	} else {
		result = "failed"
		g.ChatRecoveries[index].RetryAt = b.now().Add(notificationRetry)
		b.d.Log.Warn("Feishu recovery submission failed", "group", g.ID, "episode", n.Episode, "channel", n.Channel)
	}
	if current {
		appendNotification(g, store.Notification{At: b.now(), Channel: n.Channel, Event: "resolved", Result: result, RouteID: n.RouteID})
	} else if err == nil {
		b.d.Log.Info("Feishu accepted recovery for an earlier episode", "group", g.ID, "episode", n.Episode, "channel", n.Channel)
	}
	if err := b.d.Store.PutGroup(*g); err != nil {
		b.d.Log.Error("save Feishu recovery delivery", "group", g.ID, "episode", n.Episode, "err", err)
	}
	return true
}

// Retry after scheduled escalation and share the upkeep patch slot with session card refresh.
func (b *Bridge) retryChatRecovery(ctx context.Context, groups []store.Group, now time.Time) bool {
	if len(groups) == 0 {
		return false
	}
	start := b.chatRecoveryCursor % len(groups)
	for i := range len(groups) {
		if ctx.Err() != nil {
			return false
		}
		index := (start + i) % len(groups)
		g := &groups[index]
		if !g.Firing() && now.Sub(g.ResolvedAt) > keepResolved {
			continue // already archived by this tick
		}
		for j := range g.ChatRecoveries {
			if b.sendChatRecovery(ctx, g, j, now) {
				b.chatRecoveryCursor = (index + 1) % len(groups)
				return true
			}
		}
	}
	return false
}
