package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/notify"
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
func (b *Bridge) queueWebhook(g *store.Group, channel, event string) {
	notice := store.WebhookNotice{Delivery: newDelivery(), Event: event, Episode: g.Episode, At: b.now().UTC()}
	title, target := cleanLine(b.title(g)), b.notificationPage(g)
	if channel == config.SlackChannel {
		state := b.t("Firing")
		if event == "resolved" {
			state = b.t("Resolved")
		}
		notice.Body = notify.SlackMessage(state+" · "+b.severityName(g.Severity)+"\n"+title, target, b.t("Open alert"))
	} else {
		notice.Body, _ = json.Marshal(webhookNotice{
			Version: 1, Delivery: notice.Delivery, Event: event, At: notice.At,
			Alert: webhookAlert{ID: g.ID, Episode: g.Episode, Severity: g.Severity, Title: title, URL: target},
		})
	}
	queue := webhookQueue(g, channel)
	*queue = append(*queue, notice)
}

func webhookQueue(g *store.Group, channel string) *[]store.WebhookNotice {
	if channel == config.SlackChannel {
		return &g.SlackNotices
	}
	return &g.WebhookNotices
}

func (b *Bridge) webhookSender(channel string) WebhookSender {
	if channel == config.SlackChannel {
		return b.d.Slack
	}
	return b.d.NotificationWebhook
}

var webhookChannels = [...]string{config.NotificationWebhookChannel, config.SlackChannel}

func (b *Bridge) readyWebhook(g *store.Group, now time.Time, slackPaused bool) (string, WebhookSender, store.WebhookNotice) {
	for _, channel := range webhookChannels {
		if channel == config.SlackChannel && (b.slackActive || slackPaused) {
			continue
		}
		sender := b.webhookSender(channel)
		queue := webhookQueue(g, channel)
		if sender != nil && len(*queue) > 0 && b.expireWebhooks(g, channel, now) && len(*queue) > 0 && !(*queue)[0].RetryAt.After(now) {
			return channel, sender, (*queue)[0]
		}
	}
	return "", nil, store.WebhookNotice{}
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
	if (b.d.NotificationWebhook == nil && b.d.Slack == nil) || ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.webhookActive) >= webhooksAtOnce {
		return
	}
	now := b.now()
	slackPaused := b.d.Slack != nil && b.d.Store.SlackPausedUntil().After(now)
	for _, g := range b.d.Store.Groups() {
		if b.webhookActive[g.ID] {
			continue
		}
		channel, sender, next := b.readyWebhook(&g, now, slackPaused)
		if sender == nil {
			continue
		}
		b.webhookActive[g.ID] = true
		if channel == config.SlackChannel {
			b.slackActive = true
		}
		work.Add(1)
		go func() {
			defer work.Done()
			err := sender.Send(ctx, next.Body)
			b.finishWebhook(ctx, g.ID, channel, next, err)
		}()
		if len(b.webhookActive) == webhooksAtOnce {
			break
		}
	}
}

func (b *Bridge) expireWebhooks(g *store.Group, channel string, now time.Time) bool {
	queue := webhookQueue(g, channel)
	before := *queue
	n := 0
	for n < len(before) && !before[n].At.Add(keepResolved).After(now) {
		n++
	}
	if n == 0 {
		return true
	}
	*queue = before[n:]
	if err := b.d.Store.PutGroup(*g); err != nil {
		*queue = before
		b.d.Log.Error("expire notification webhooks", "group", g.ID, "channel", channel, "err", err)
		return false
	}
	b.auditExpiredWebhooks(g.ID, channel, n)
	return true
}

func (b *Bridge) auditExpiredWebhooks(group, channel string, n int) {
	if n == 0 {
		return
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: "beacon", Via: channel,
		Action: "notify_people", Target: group, Detail: fmt.Sprintf("%d queued webhook deliveries expired", n)})
}

func (b *Bridge) finishWebhook(ctx context.Context, id, channel string, notice store.WebhookNotice, sendErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.webhookActive, id)
	now := b.now()
	var ratePause time.Duration
	if channel == config.SlackChannel {
		b.slackActive = false
		if limited, ok := errors.AsType[notify.SlackRateLimited](sendErr); ok && limited.After > 0 {
			ratePause = limited.After
			// A known pause applies to the whole destination even if leadership just ended.
			if err := b.d.Store.ExtendSlackPause(now.Add(ratePause)); err != nil {
				b.d.Log.Error("save Slack rate limit", "group", id, "err", err)
			}
		}
	}
	// The next leader can resume the same delivery after an interrupted request.
	if ctx.Err() != nil {
		return
	}
	g, ok := b.d.Store.Group(id)
	queue := webhookQueue(&g, channel)
	if !ok || len(*queue) == 0 || (*queue)[0].Delivery != notice.Delivery {
		b.wakeWebhooks()
		return
	}
	current := g.Episode == notice.Episode
	result := "ok"
	if sendErr != nil {
		result = "error"
		(*queue)[0].RetryAt = now.Add(max(notificationRetry, ratePause))
		b.d.Log.Warn("notification webhook failed", "group", g.ID, "channel", channel, "delivery", notice.Delivery, "err", sendErr)
	} else {
		*queue = (*queue)[1:]
		if current && notice.Event == "firing" {
			g.Channel, g.ChannelAt = channel, now
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

func (b *Bridge) failedWebhookDeliveries(now time.Time) (webhook, slack bool) {
	for _, g := range b.d.Store.Groups() {
		webhook = webhook || (b.d.NotificationWebhook != nil && failedWebhook(g.WebhookNotices, now))
		slack = slack || (b.d.Slack != nil && failedWebhook(g.SlackNotices, now))
	}
	return webhook, slack
}

func failedWebhook(queue []store.WebhookNotice, now time.Time) bool {
	return len(queue) > 0 && !queue[0].RetryAt.IsZero() && queue[0].At.Add(keepResolved).After(now)
}
