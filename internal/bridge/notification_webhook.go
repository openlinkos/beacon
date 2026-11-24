package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

// WebhookSender submits a signed human notification to the configured receiver.
type WebhookSender interface {
	Send(context.Context, []byte) error
}

const webhooksAtOnce = 8

type webhookAlert struct {
	ID       string `json:"id"`
	Episode  string `json:"episode"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
}

type webhookNotice struct {
	Version  int          `json:"version"`
	Delivery string       `json:"delivery"`
	Event    string       `json:"event"`
	At       time.Time    `json:"at"`
	Alert    webhookAlert `json:"alert"`
}

// queueWebhook snapshots the notice with the group's transition. Its body and delivery ID stay the
// same across retries, including when another episode begins before the receiver is reachable.
func (b *Bridge) queueWebhook(g *store.Group, event string) {
	notice := webhookNotice{
		Version: 1, Delivery: newDelivery(), Event: event, At: b.now().UTC(),
		Alert: webhookAlert{ID: g.ID, Episode: g.Episode, Severity: g.Severity, Title: cleanLine(b.title(g)), URL: b.notificationPage(g)},
	}
	body, _ := json.Marshal(notice)
	g.WebhookNotices = append(g.WebhookNotices, store.WebhookNotice{
		Delivery: notice.Delivery, Event: notice.Event, Episode: g.Episode, At: notice.At, Body: body,
	})
}

// wakeWebhooks prompts the leader's bridge loop after the alert transition has been stored.
func (b *Bridge) wakeWebhooks() {
	select {
	case b.webhookWake <- struct{}{}:
	default:
	}
}

// sendWebhooks starts at most one notice per group. Taking only the head preserves the order of
// firing and recovery, and sending outside mu leaves the alert and agent paths free to proceed.
func (b *Bridge) sendWebhooks(ctx context.Context, work *sync.WaitGroup) {
	if b.d.NotificationWebhook == nil || ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.webhookActive) >= webhooksAtOnce {
		return
	}
	now := b.now()
	for _, g := range b.d.Store.Groups() {
		if len(g.WebhookNotices) == 0 || b.webhookActive[g.ID] {
			continue
		}
		if !b.expireWebhooks(&g, now) || len(g.WebhookNotices) == 0 {
			continue
		}
		next := g.WebhookNotices[0]
		if next.RetryAt.After(now) {
			continue
		}
		b.webhookActive[g.ID] = true
		work.Add(1)
		go func() {
			defer work.Done()
			err := b.d.NotificationWebhook.Send(ctx, next.Body)
			b.finishWebhook(ctx, g.ID, next, err)
		}()
		if len(b.webhookActive) == webhooksAtOnce {
			break
		}
	}
}

func (b *Bridge) expireWebhooks(g *store.Group, now time.Time) bool {
	n := 0
	for n < len(g.WebhookNotices) && !g.WebhookNotices[n].At.Add(keepResolved).After(now) {
		n++
	}
	if n == 0 {
		return true
	}
	g.WebhookNotices = g.WebhookNotices[n:]
	if err := b.d.Store.PutGroup(*g); err != nil {
		b.d.Log.Error("expire notification webhooks", "group", g.ID, "err", err)
		return false
	}
	b.auditExpiredWebhooks(g.ID, n)
	return true
}

func (b *Bridge) auditExpiredWebhooks(group string, n int) {
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: "beacon", Via: config.NotificationWebhookChannel,
		Action: "notify_people", Target: group, Detail: fmt.Sprintf("%d queued webhook deliveries expired", n)})
}

func (b *Bridge) finishWebhook(ctx context.Context, id string, notice store.WebhookNotice, sendErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.webhookActive, id)
	// The next leader can resume the same delivery after an interrupted request.
	if ctx.Err() != nil {
		return
	}
	g, ok := b.d.Store.Group(id)
	if !ok || len(g.WebhookNotices) == 0 || g.WebhookNotices[0].Delivery != notice.Delivery {
		b.wakeWebhooks()
		return
	}
	const channel = config.NotificationWebhookChannel
	current := g.Episode == notice.Episode
	result := "ok"
	if sendErr != nil {
		result = "error"
		g.WebhookNotices[0].RetryAt = b.now().Add(notificationRetry)
		b.d.Log.Warn("notification webhook failed", "group", g.ID, "delivery", notice.Delivery, "err", sendErr)
	} else {
		g.WebhookNotices = g.WebhookNotices[1:]
		if current && notice.Event == "firing" {
			g.Channel, g.ChannelAt = channel, b.now()
		}
	}
	b.count(channel, result)
	if current {
		b.recordNotification(&g, channel, sendErr)
	}
	if err := b.d.Store.PutGroup(g); err != nil {
		b.d.Log.Error("save notification webhook result", "group", g.ID, "err", err)
		return
	}
	if !current {
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: "beacon", Via: channel, Action: "notify_people",
			Target: g.ID, Detail: fmt.Sprintf("older episode delivery %s (%s): %s", notice.Delivery, notice.Event, result)})
	}
	b.wakeWebhooks()
}

func (b *Bridge) webhookWaitingOnFailure(now time.Time) bool {
	for _, g := range b.d.Store.Groups() {
		if len(g.WebhookNotices) > 0 {
			next := g.WebhookNotices[0]
			if !next.RetryAt.IsZero() && next.At.Add(keepResolved).After(now) {
				return true
			}
		}
	}
	return false
}
