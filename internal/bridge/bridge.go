// Package bridge closes the loop between the alerts and the people on call.
//
// The router hands each alert group to the bridge. A new group becomes a Feishu card in the alert
// chat (or a plain message from the group's custom bot, for low severities or when the app's API
// quota runs low). Buttons on the card acknowledge the alert or silence it; the card
// changes in place, and turns green when the alert resolves. A critical alert nobody acknowledges is
// escalated: a buzz in the Feishu app, then a Pushover emergency alert that repeats on the phone until
// someone acknowledges it there (which also counts as an acknowledgement here).
//
// The same operations are offered to the REST API and the MCP server, so an agent can see and act on
// alerts; their writes are audited and echoed to the chat.
package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/notify"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
	"github.com/openlinkos/beacon/internal/web/link"
)

// Silencer is what the bridge needs from the router: creating, ending and listing silences.
type Silencer interface {
	CreateSilence(ctx context.Context, labels map[string]string, start, end time.Time, createdBy, comment string) (string, error)
	ExpireSilence(ctx context.Context, id string) error
	Silences(ctx context.Context) ([]alertmanager.Silence, error)
}

// Pusher is Pushover's emergency priority.
type Pusher interface {
	Send(ctx context.Context, m notify.Emergency) (string, error)
	Receipt(ctx context.Context, receipt string) (notify.Receipt, error)
	CancelByTag(ctx context.Context, tag string) error
}

// Barker is a ringing iOS push.
type Barker interface {
	Send(ctx context.Context, title, body, link string) error
}

// Poster posts plain text to the alert chat.
type Poster interface {
	Send(ctx context.Context, text string) error
}

// Querier evaluates instant queries (for the per-location matrix on cards).
type Querier interface {
	Query(ctx context.Context, query string) ([]vm.Sample, error)
}

// Deps are the bridge's collaborators. Notification providers and VM may be nil.
type Deps struct {
	Config              *config.Config
	Store               *store.Store
	AM                  Silencer
	Feishu              feishu.API
	Pushover            Pusher
	Bark                Barker
	Bot                 Poster
	NotificationWebhook WebhookSender
	Slack               WebhookSender
	VM                  Querier
	Catalog             func() *catalog.Catalog
	MonitorMachine      func(service, severity string, started time.Time) (store.MonitorMachine, bool)
	Metrics             *metrics.Metrics
	Log                 *slog.Logger
	Now                 func() time.Time
	// WSReady reports whether Feishu's long connection is up; nil when callbacks come over HTTP.
	WSReady func() bool
	// ProbeNow is the board of requests to probe on demand (probenow.go); nil without a gateway.
	ProbeNow *probenow.Board
	// Pages builds the signed links to the pages people open from the chat (pages.go); nil: no links.
	Pages *link.Pages
	// ShellCommands reads, from a finished session's transcript, the commands run at an interactive
	// shell's prompt with their exit statuses; nil without the SSH channel.
	ShellCommands func(sessionID string) []ShellCommand
	// SSHConfig is the ssh configuration (ssh_config blocks and the known_hosts line) for the principal with
	// this key (store.Principal.Key) and optional machine; nil without the SSH channel.
	SSHConfig func(key, machine string) string
	// Recheck closes what is open on the SSH channel for whoever may no longer reach it, after a
	// credential, an agent or an account changed (sshgate.Gate.Recheck); nil without the channel.
	Recheck func() int
}

// Bridge is the alert bridge.
type Bridge struct {
	d       Deps
	lang    i18n.Lang
	started time.Time

	mu            sync.Mutex // serialises every change to the groups
	watchdogAt    atomic.Int64
	feishuFail    atomic.Int64 // unix nanoseconds of the last failed Feishu call, 0 after a success
	hooks         hooks        // calls that wake agents, in flight (agents.go)
	webhookWake   chan struct{}
	webhookActive map[string]bool // one in-flight notice per group, protected by mu
	slackActive   bool            // one request at a time to the Slack channel, protected by mu

	sessionCardCursor int // next group to check for session changes, protected by mu
}

const (
	escalationEvery = 2 * time.Second
	upkeepEvery     = 15 * time.Second
	patchEvery      = 5 * time.Minute
	receiptEvery    = 30 * time.Second
	keepResolved    = 7 * 24 * time.Hour
	watchdogMax     = 3 * time.Minute
	maxSilence      = 24 * time.Hour
)

// New creates a bridge.
func New(d Deps) *Bridge {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Catalog == nil {
		d.Catalog = func() *catalog.Catalog { return &catalog.Catalog{} }
	}
	lang := i18n.ZH
	if d.Config.Language == "en" {
		lang = i18n.EN
	}
	b := &Bridge{d: d, lang: lang, started: d.Now()}
	if d.NotificationWebhook != nil || d.Slack != nil {
		b.webhookWake = make(chan struct{}, 1)
		b.webhookActive = make(map[string]bool)
	}
	if d.Metrics != nil {
		d.Metrics.Register("beacon_bridge_ready", "1 when the bridge can deliver alerts", "gauge", func() []metrics.Sample {
			ok, _ := b.Ready()
			return []metrics.Sample{{Value: map[bool]float64{true: 1, false: 0}[ok]}}
		})
		d.Metrics.Register("beacon_feishu_api_calls", "Feishu app API calls this month", "gauge", func() []metrics.Sample {
			return []metrics.Sample{{Value: float64(b.d.Store.Counter(b.quotaKey()))}}
		})
		d.Metrics.Register("beacon_alert_groups_firing", "Alert groups firing, by severity", "gauge", func() []metrics.Sample {
			n := map[string]int{}
			for _, g := range b.d.Store.Groups() {
				if g.Firing() {
					n[g.Severity]++
				}
			}
			var out []metrics.Sample
			for _, s := range config.Severities {
				out = append(out, metrics.Sample{Labels: metrics.Labels("severity", s), Value: float64(n[s])})
			}
			return out
		})
	}
	return b
}

// GroupID is the short, stable id of a group key.
func GroupID(groupKey string) string {
	sum := sha256.Sum256([]byte(groupKey))
	return hex.EncodeToString(sum[:])[:12]
}

func (b *Bridge) now() time.Time { return b.d.Now() }

func (b *Bridge) count(channel, result string) {
	if b.d.Metrics != nil {
		b.d.Metrics.Inc("beacon_notifications_total", "Notifications sent, by channel and result", metrics.Labels("channel", channel, "result", result))
	}
}

// CountFeishuCall records one Feishu API call against this month's budget.
func (b *Bridge) CountFeishuCall() { b.d.Store.Add(b.quotaKey(), 1) }

func (b *Bridge) quotaKey() string {
	return "feishu-calls-" + b.now().In(b.d.Config.Location).Format("2006-01")
}

// quota is the share of the month's Feishu API budget already used.
func (b *Bridge) quota() float64 {
	return float64(b.d.Store.Counter(b.quotaKey())) / float64(b.d.Config.Feishu.MonthlyBudget)
}

// appAllowed says whether a card for severity may use the app: above 70% of the budget only critical
// alerts do, and at 100% none.
func (b *Bridge) appAllowed(severity string) bool {
	if b.d.Feishu == nil {
		return false
	}
	q := b.quota()
	switch {
	case q >= 1:
		return false
	case q >= 0.7:
		return severity == "critical"
	}
	return true
}

// patchAllowed says whether a card may be updated by an API call (clicks update it for free).
func (b *Bridge) patchAllowed(g *store.Group, resolving bool) bool {
	if b.d.Feishu == nil || g.Channel != "app" || g.MessageID == "" {
		return false
	}
	q := b.quota()
	if resolving {
		return q < 1
	}
	return q < 0.9
}

func (b *Bridge) feishuResult(err error) {
	if err != nil {
		b.feishuFail.Store(b.now().UnixNano())
		return
	}
	b.feishuFail.Store(0)
}

// ---- Webhook

// Watchdog records that the rules pipeline delivered its Watchdog alert.
func (b *Bridge) Watchdog() { b.watchdogAt.Store(b.now().UnixNano()) }

// HandleWebhook takes one alert group (from the router, in Alertmanager's webhook form).
func (b *Bridge) HandleWebhook(ctx context.Context, w alertmanager.Webhook) error {
	if w.GroupLabels["alertname"] == "Watchdog" || w.CommonLabels["alertname"] == "Watchdog" {
		b.watchdogAt.Store(b.now().UnixNano())
		return nil
	}
	sev := w.CommonLabels["severity"]
	if sev == "none" {
		return nil
	}
	if !slices.Contains(config.Severities, sev) {
		sev = "warning"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id := GroupID(w.GroupKey)
	g, exists := b.d.Store.Group(id)
	wasFiring := exists && g.Firing()
	var previous *store.IncidentOutcome
	if w.Status == "firing" && exists && !wasFiring {
		now := b.now()
		previous = b.archiveOutcome(g, now, now.Add(-time.Nanosecond))
	}
	g.ID, g.Key, g.Severity = id, w.GroupKey, sev
	g.Labels, g.Common, g.Annotations = w.GroupLabels, w.CommonLabels, w.CommonAnnotations
	g.Alerts = g.Alerts[:0]
	var starts time.Time
	for _, a := range w.Alerts {
		g.Alerts = append(g.Alerts, store.Alert{Fingerprint: a.Fingerprint, Status: a.Status, Labels: a.Labels, Annotations: a.Annotations, StartsAt: a.StartsAt, GeneratorURL: a.GeneratorURL})
		if a.Status == "firing" && (starts.IsZero() || a.StartsAt.Before(starts)) {
			starts = a.StartsAt
		}
	}
	switch w.Status {
	case "firing":
		g.Status = "firing"
		if !wasFiring {
			g.Ack, g.Silence, g.Notes, g.Agents = nil, nil, nil, nil
			g.ProbeRuns, g.PriorAcks, g.Notifications = nil, nil, nil
			g.NotifyWebhook, g.NotifySlack = false, false
			g.Episode, g.Participation = newDelivery(), nil
			g.MonitorMachines = b.incidentMonitorMachines(&g)
			g.Buzzed, g.Pushed, g.Barked, g.ReceiptAt, g.ResolvedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
			g.ChannelAt = time.Time{}
			g.Receipt, g.MessageID, g.CardHash, g.Channel = "", "", "", ""
			g.CardSessionHash, g.CardSessionAttemptAt = "", time.Time{}
			g.StartsAt = starts
			if g.StartsAt.IsZero() {
				g.StartsAt = b.now()
			}
			b.notifyNew(ctx, &g)
			b.wake(&g, "firing")
		} else {
			b.refresh(ctx, &g)
		}
	case "resolved":
		if !wasFiring {
			return nil
		}
		g.Status = "resolved"
		g.ResolvedAt = b.now()
		b.resolve(ctx, &g)
		if g.NotifyWebhook {
			b.queueWebhook(&g, config.NotificationWebhookChannel, "resolved")
		}
		if g.NotifySlack {
			b.queueWebhook(&g, config.SlackChannel, "resolved")
		}
		b.wake(&g, "resolved")
		err := b.saveGroup(g)
		if err == nil && (len(g.WebhookNotices) > 0 || len(g.SlackNotices) > 0) {
			b.wakeWebhooks()
		}
		return err
	}
	err := b.d.Store.PutGroupWithOutcome(g, previous)
	if err == nil && (len(g.WebhookNotices) > 0 || len(g.SlackNotices) > 0) {
		b.wakeWebhooks()
	}
	return err
}

func (b *Bridge) notifyNew(ctx context.Context, g *store.Group) {
	g.NotifiedAt = b.now()
	pol := b.d.Config.Policy(g.Severity)
	g.Channel = "none"
	if pol.InitialPushover && b.d.Pushover != nil {
		b.push(ctx, g)
		return
	}
	if pol.Channel == config.NotificationWebhookChannel || pol.Channel == config.SlackChannel {
		g.NotifyWebhook = pol.Channel == config.NotificationWebhookChannel
		g.NotifySlack = pol.Channel == config.SlackChannel
		b.queueWebhook(g, pol.Channel, "firing")
		return
	}
	if pol.Channel != "app" && pol.Channel != "webhook" {
		b.d.Log.Info("alert recorded without an initial human notification", "group", g.ID, "summary", b.title(g))
		return
	}
	b.tryChat(ctx, g, pol)
	if g.Channel == "none" && g.Pushed.IsZero() {
		b.d.Log.Error("alert could not be delivered by its initial routes", "group", g.ID, "summary", b.title(g))
	}
}

// tryChat sends through the existing Feishu routes. The same group and delivery identity are
// retained on retry, and NotifiedAt continues to govern scheduled phone escalation.
func (b *Bridge) tryChat(ctx context.Context, g *store.Group, pol config.Policy) bool {
	now := b.now()
	attempted := false
	phoneFallback := func() {
		if g.Severity == "critical" && b.d.Pushover != nil && !pushoverAccepted(g) && notificationDue(g, "pushover", b.now()) {
			b.push(ctx, g)
			attempted = true
		}
	}
	if pol.Channel == "app" && b.appAllowed(g.Severity) && notificationDue(g, "app", now) {
		attempted = true
		sessions := b.sessionSummary(g)
		episode := g.Episode
		if episode == "" {
			episode = g.StartsAt.UTC().Format("20060102T150405")
		}
		id, err := b.d.Feishu.SendCard(ctx, b.d.Config.Feishu.ChatID, b.cardWithSessions(ctx, g, sessions), g.ID+"-"+episode)
		if err == nil && id == "" {
			err = errors.New("feishu send: response did not confirm acceptance")
		}
		b.feishuResult(err)
		b.recordNotification(g, "app", err)
		if err == nil {
			g.Channel, g.MessageID, g.CardAt, g.CardHash = "app", id, b.now(), cardHash(g)
			g.ChannelAt = g.CardAt
			g.CardSessionHash = sessions.fingerprint()
			b.count("feishu", "ok")
			return true
		}
		b.count("feishu", "error")
		b.d.Log.Error("feishu card failed; falling back", "group", g.ID, "err", err)
		phoneFallback()
	}
	if b.d.Bot != nil && notificationDue(g, "webhook", now) {
		attempted = true
		err := b.d.Bot.Send(ctx, b.plainFiring(g))
		b.recordNotification(g, "webhook", err)
		if err == nil {
			g.Channel, g.ChannelAt = "webhook", b.now()
			b.count("feishu-bot", "ok")
			return true
		}
		b.count("feishu-bot", "error")
		b.d.Log.Error("feishu group bot failed", "group", g.ID, "err", err)
	}
	phoneFallback()
	return attempted
}

func (b *Bridge) chatRetryDue(g *store.Group, pol config.Policy, now time.Time) bool {
	if g.Channel != "none" || pol.InitialPushover || g.NotifiedAt.IsZero() ||
		now.Sub(g.NotifiedAt) < notificationRetry || (pol.Channel != "app" && pol.Channel != "webhook") {
		return false
	}
	return (pol.Channel == "app" && b.appAllowed(g.Severity) && notificationDue(g, "app", now)) ||
		(b.d.Bot != nil && notificationDue(g, "webhook", now))
}

// refresh updates the card when the alerts in the group changed, at most every five minutes.
func (b *Bridge) refresh(ctx context.Context, g *store.Group) {
	h := cardHash(g)
	if h == g.CardHash || b.now().Sub(g.CardAt) < patchEvery || !b.patchAllowed(g, false) {
		return
	}
	b.patch(ctx, g)
}

func (b *Bridge) patch(ctx context.Context, g *store.Group) {
	b.patchWithSessions(ctx, g, b.sessionSummary(g))
}

func (b *Bridge) patchWithSessions(ctx context.Context, g *store.Group, sessions cardSessions) {
	err := b.d.Feishu.PatchCard(ctx, g.MessageID, b.cardWithSessions(ctx, g, sessions))
	b.feishuResult(err)
	if err != nil {
		b.count("feishu-patch", "error")
		b.d.Log.Warn("feishu card update failed", "group", g.ID, "err", err)
		return
	}
	b.count("feishu-patch", "ok")
	g.CardAt, g.CardHash = b.now(), cardHash(g)
	g.CardSessionHash = sessions.fingerprint()
}

func (b *Bridge) resolve(ctx context.Context, g *store.Group) {
	if pushoverAccepted(g) && b.d.Pushover != nil && !g.Ack.StopsEscalation() {
		b.cancelPushover(ctx, g)
	}
	switch {
	case b.patchAllowed(g, true):
		b.patch(ctx, g)
	case g.Channel == "webhook" && b.d.Bot != nil:
		if err := b.d.Bot.Send(ctx, b.plainResolved(g)); err != nil {
			b.d.Log.Warn("feishu group bot failed", "group", g.ID, "err", err)
		}
	}
}

func (b *Bridge) push(ctx context.Context, g *store.Group) {
	if !notificationDue(g, "pushover", b.now()) {
		return
	}
	target, label := b.phoneLink(g)
	rc, err := b.d.Pushover.Send(ctx, notify.Emergency{
		Title: b.t("%s: %s", b.severityName(g.Severity), b.title(g)), Message: b.plainBody(g),
		URL: target, URLTitle: label, Tag: g.ID,
	})
	b.recordNotification(g, "pushover", err)
	if err != nil {
		b.count("pushover", "error")
		b.d.Log.Error("pushover failed", "group", g.ID, "err", err)
		return
	}
	b.count("pushover", "ok")
	g.Pushed, g.Receipt, g.ReceiptAt = b.now(), rc, b.now()
}

// ---- Clicks

// HandleAction answers a click on a card.
func (b *Bridge) HandleAction(ctx context.Context, a feishu.Action) feishu.Response {
	person, ok := b.d.Config.OnCallByID[a.OpenID]
	if !ok {
		return feishu.Response{Toast: b.t("Only the people on call can act on alerts"), ToastKind: "error"}
	}
	act, _ := a.Value["a"].(string)
	id, _ := a.Value["g"].(string)
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok {
		return feishu.Response{Toast: b.t("Unknown alert; it may have been cleaned up"), ToastKind: "warning"}
	}
	if !currentCardAction(&g, a) {
		return feishu.Response{Toast: b.t("Open the current alert card to act."), ToastKind: "warning"}
	}
	if !g.Firing() {
		return feishu.Response{Toast: b.t("This alert has already resolved"), ToastKind: "info", Card: b.card(ctx, &g)}
	}
	var toast string
	who := Actor{Name: person.Name, Via: "feishu"}
	switch act {
	case "ack", "note":
		note := ""
		if act == "note" {
			note, _ = a.Form["note"].(string)
			note = strings.TrimSpace(note)
		}
		b.ack(ctx, &g, who, note)
		toast = b.t("Acknowledged")
	case "silence":
		d, err := time.ParseDuration(fmt.Sprint(a.Value["d"]))
		if err != nil || d <= 0 || d > maxSilence {
			return feishu.Response{Toast: b.t("Unknown action"), ToastKind: "error"}
		}
		if err := b.silence(ctx, &g, d, who, "from the Feishu card"); err != nil {
			b.d.Log.Error("silence failed", "group", g.ID, "err", err)
			return feishu.Response{Toast: b.t("Could not silence: %s", err.Error()), ToastKind: "error"}
		}
		toast = b.t("Silenced for %s", b.dur(d))
	default:
		return feishu.Response{Toast: b.t("Unknown action"), ToastKind: "error"}
	}
	action := "ack"
	if act == "silence" {
		action = "silence"
	}
	recordParticipation(&g, who, action, b.now())
	sessions := b.sessionSummary(&g)
	card := b.cardWithSessions(ctx, &g, sessions)
	g.CardAt, g.CardHash = b.now(), cardHash(&g)
	g.CardSessionHash = sessions.fingerprint()
	if err := b.d.Store.PutGroup(g); err != nil {
		b.d.Log.Error("save group", "err", err)
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: person.Name, Via: "feishu", Action: act, Target: g.ID, Detail: b.title(&g)})
	return feishu.Response{Toast: toast, ToastKind: "success", Card: card}
}

func (b *Bridge) ack(ctx context.Context, g *store.Group, who Actor, note string) {
	a := store.Ack{By: who.Name, Via: who.Via, Note: note, At: b.now(), Kind: actorKind(who)}
	if a.Kind == "agent" && g.Ack.StopsEscalation() {
		rememberAck(g, a)
		return
	}
	if g.Ack != nil {
		rememberAck(g, *g.Ack)
	}
	g.Ack = &a
	if a.Kind != "agent" {
		b.cancelPushover(ctx, g)
	}
}

func (b *Bridge) cancelPushover(ctx context.Context, g *store.Group) {
	if pushoverAccepted(g) && b.d.Pushover != nil {
		if err := b.d.Pushover.CancelByTag(ctx, g.ID); err != nil {
			b.d.Log.Warn("pushover cancel failed", "group", g.ID, "err", err)
		}
	}
}

func rememberAck(g *store.Group, a store.Ack) {
	a.Note = clean(a.Note, maxLong, true)
	g.PriorAcks = append(g.PriorAcks, a)
	if n := len(g.PriorAcks); n > 20 {
		g.PriorAcks = g.PriorAcks[n-20:]
	}
}

func ackVersion(g *store.Group) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%q %s", g.Episode, g.StartsAt.UTC().Format(time.RFC3339Nano))
	if a := g.Ack; a != nil {
		_, _ = fmt.Fprintf(h, " %q %q %q %q %s", a.By, a.Via, a.Note, a.Kind, a.At.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (b *Bridge) silence(ctx context.Context, g *store.Group, d time.Duration, who Actor, comment string) error {
	labels := g.Labels
	if len(labels) == 0 {
		labels = map[string]string{"alertname": g.Common["alertname"]}
	}
	now := b.now()
	id, err := b.d.AM.CreateSilence(ctx, labels, now, now.Add(d), who.Name, comment)
	if err != nil {
		return err
	}
	b.markSilence(ctx, g, store.Silence{ID: id, By: who.Name, Until: now.Add(d)}, who)
	return nil
}

func (b *Bridge) markSilence(ctx context.Context, g *store.Group, silence store.Silence, who Actor) {
	g.Silence = &silence
	if g.Ack == nil {
		b.ack(ctx, g, who, "")
	}
	if !g.Ack.StopsEscalation() {
		b.cancelPushover(ctx, g)
		g.Pushed, g.Receipt, g.ReceiptAt = time.Time{}, "", time.Time{}
	}
}

// ---- Escalation and upkeep

// Tick runs timed escalation and upkeep, including receipt polling and retaining incident history.
func (b *Bridge) Tick(ctx context.Context) {
	b.tick(ctx, true)
}

func (b *Bridge) tick(ctx context.Context, upkeep bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	ttl := b.d.Config.AckTTL.D()
	groups := b.d.Store.Groups()
	retryChat := -1
	for i, g := range groups {
		if ctx.Err() != nil {
			return
		}
		now := b.now()
		if !g.Firing() {
			if upkeep && now.Sub(g.ResolvedAt) > keepResolved {
				if err := b.d.Store.DropResolvedGroup(g, b.archiveOutcome(g, now, now)); err != nil {
					b.d.Log.Error("archive resolved alert group", "group", g.ID, "err", err)
				} else {
					b.auditExpiredWebhooks(g.ID, config.NotificationWebhookChannel, len(g.WebhookNotices))
					b.auditExpiredWebhooks(g.ID, config.SlackChannel, len(g.SlackNotices))
				}
			}
			continue
		}
		changed := false
		pol := b.d.Config.Policy(g.Severity)
		silenced := g.Silence != nil && g.Silence.Until.After(now)
		if g.Ack != nil && now.Sub(g.Ack.At) > ttl && !silenced {
			resume := g.Ack.StopsEscalation()
			rememberAck(&g, *g.Ack)
			g.Ack, g.Silence = nil, nil
			if resume {
				b.d.Log.Info("acknowledgement lapsed; escalating again", "group", g.ID)
				if g.ChannelAt.IsZero() && (g.Channel == "app" || g.Channel == "webhook") {
					g.ChannelAt = g.NotifiedAt
				}
				g.NotifiedAt, g.Buzzed, g.Pushed, g.Barked, g.Receipt = now, time.Time{}, time.Time{}, time.Time{}, ""
				g.ReceiptAt = time.Time{}
			}
			if b.patchAllowed(&g, false) {
				b.patch(ctx, &g)
			}
			changed = true
		}
		canEscalate := !g.Ack.StopsEscalation() && !silenced
		if canEscalate && !g.NotifiedAt.IsZero() {
			since := now.Sub(g.NotifiedAt)
			if pol.Buzz > 0 && g.Buzzed.IsZero() && g.MessageID != "" && b.d.Feishu != nil && since >= pol.Buzz.D() && b.quota() < 1 &&
				notificationDue(&g, "feishu-buzz", now) {
				err := b.d.Feishu.UrgentApp(ctx, g.MessageID, b.onCallIDs())
				b.recordNotification(&g, "feishu-buzz", err)
				b.feishuResult(err)
				if err != nil {
					b.count("feishu-buzz", "error")
					b.d.Log.Warn("feishu buzz failed", "group", g.ID, "err", err)
				} else {
					b.count("feishu-buzz", "ok")
					g.Buzzed = now
					if b.patchAllowed(&g, false) {
						b.patch(ctx, &g)
					}
				}
				changed = true
			}
			lastPush := latestNotification(&g, "pushover")
			retryInitial := g.Severity == "critical" && (pol.Channel != "none" || pol.InitialPushover) && lastPush.Result == "failed" && !lastPush.At.Before(g.NotifiedAt)
			if b.d.Pushover != nil && !pushoverAccepted(&g) && (retryInitial || (pol.Pushover > 0 && since >= pol.Pushover.D())) &&
				notificationDue(&g, "pushover", now) {
				b.push(ctx, &g)
				changed = true
			}
			if pol.Bark > 0 && g.Barked.IsZero() && b.d.Bark != nil && since >= pol.Bark.D() &&
				notificationDue(&g, "bark", now) {
				target, _ := b.phoneLink(&g)
				err := b.d.Bark.Send(ctx, b.t("%s: %s", b.severityName(g.Severity), b.title(&g)), b.plainBody(&g), target)
				b.recordNotification(&g, "bark", err)
				if err != nil {
					b.count("bark", "error")
					b.d.Log.Warn("bark failed", "group", g.ID, "err", err)
				} else {
					b.count("bark", "ok")
					g.Barked = now
				}
				changed = true
			}
		}
		if upkeep && canEscalate && g.Receipt != "" && b.d.Pushover != nil && now.Sub(g.ReceiptAt) >= receiptEvery {
			g.ReceiptAt, changed = now, true
			r, err := b.d.Pushover.Receipt(ctx, g.Receipt)
			switch {
			case err != nil:
				b.d.Log.Warn("pushover receipt failed", "group", g.ID, "err", err)
			case r.Acknowledged:
				by := "Pushover"
				if r.Device != "" {
					by += " (" + r.Device + ")"
				}
				if g.Ack != nil {
					rememberAck(&g, *g.Ack)
				}
				g.Ack = &store.Ack{By: by, Via: "pushover", At: r.At, Kind: "person"}
				if g.Ack.At.IsZero() {
					g.Ack.At = now
				}
				recordParticipation(&g, Actor{Name: by, Via: "pushover"}, "ack", g.Ack.At)
				_ = b.d.Store.Audit(store.AuditEntry{Actor: by, Via: "pushover", Action: "ack", Target: g.ID, Detail: b.title(&g)})
				if b.patchAllowed(&g, false) {
					b.patch(ctx, &g)
				}
			case r.Expired:
				g.Receipt = ""
			}
		}
		if changed {
			if err := b.d.Store.PutGroup(g); err != nil {
				b.d.Log.Error("save group", "err", err)
			}
			groups[i] = g
		}
		if !silenced && !g.Ack.StopsEscalation() && b.chatRetryDue(&g, pol, now) &&
			(retryChat < 0 || g.Severity == "critical" && groups[retryChat].Severity != "critical") {
			retryChat = i
		}
	}
	// Try one group after checking every scheduled phone route so chat retries cannot take their turn.
	if retryChat >= 0 && ctx.Err() == nil {
		g := &groups[retryChat]
		if b.tryChat(ctx, g, b.d.Config.Policy(g.Severity)) {
			if err := b.d.Store.PutGroup(*g); err != nil {
				b.d.Log.Error("save retried chat delivery", "group", g.ID, "err", err)
			}
		}
	}
	if upkeep {
		b.refreshSessionCards(ctx, groups, b.now())
	}
}

func (b *Bridge) onCallIDs() []string {
	ids := make([]string, 0, len(b.d.Config.Feishu.OnCall))
	for _, p := range b.d.Config.Feishu.OnCall {
		ids = append(ids, p.OpenID)
	}
	return ids
}

// Ready checks the alerting pipeline's Watchdog and the configured notification providers.
func (b *Bridge) Ready() (bool, []string) {
	var why []string
	now := b.now()
	wd := b.watchdogAt.Load()
	switch {
	case wd == 0 && now.Sub(b.started) > watchdogMax:
		why = append(why, "no Watchdog from the rule engine since start")
	case wd != 0 && now.Sub(time.Unix(0, wd)) > watchdogMax:
		why = append(why, "the Watchdog from the rule engine stopped")
	}
	if b.d.Feishu != nil {
		if b.d.WSReady != nil && !b.d.WSReady() {
			why = append(why, "the Feishu long connection is down")
		}
		if f := b.feishuFail.Load(); f != 0 && now.Sub(time.Unix(0, f)) < 10*time.Minute {
			why = append(why, "a Feishu API call failed in the last 10 minutes")
		}
		if b.quota() >= 1 {
			why = append(why, "the Feishu API budget for this month is used up")
		}
	}
	if b.d.NotificationWebhook != nil || b.d.Slack != nil {
		failedWebhook, failedSlack := b.failedWebhookDeliveries(now)
		if failedWebhook {
			why = append(why, "the notification webhook has a failed delivery waiting to retry")
		}
		if failedSlack {
			why = append(why, "Slack has a failed delivery waiting to retry")
		}
	}
	return len(why) == 0, why
}

// Run ticks until ctx is done and pings the healthchecks.io check every minute when ready.
func (b *Bridge) Run(ctx context.Context, ping func(ctx context.Context, ok bool) error) {
	var background sync.WaitGroup
	defer background.Wait()
	b.resumeWakes(ctx)
	b.initOutcomes()
	b.sendWebhooks(ctx, &background)
	if ping != nil {
		background.Go(func() { b.runHealthchecks(ctx, ping) })
	}
	tick := time.NewTicker(upkeepEvery)
	defer tick.Stop()
	var escalate <-chan time.Time
	if b.hasTimedEscalation() {
		t := time.NewTicker(escalationEvery)
		defer t.Stop()
		escalate = t.C
	}
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-escalate:
			b.tick(ctx, false)
		case <-tick.C:
			b.Tick(ctx)
			b.sendWebhooks(ctx, &background)
		case <-b.webhookWake:
			b.sendWebhooks(ctx, &background)
		case <-prune.C:
			b.pruneOutcomes()
		}
	}
}

func (b *Bridge) hasTimedEscalation() bool {
	if b.d.Pushover != nil {
		return true // even an initial push can need a retry
	}
	for _, p := range b.d.Config.Escalation {
		if (b.d.Feishu != nil && p.Buzz > 0) || (b.d.Bark != nil && p.Bark > 0) {
			return true
		}
	}
	return false
}

func (b *Bridge) runHealthchecks(ctx context.Context, ping func(context.Context, bool) error) {
	beat := time.NewTicker(time.Minute)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			if ctx.Err() != nil {
				return
			}
			ok, why := b.Ready()
			if !ok {
				b.d.Log.Warn("bridge not ready", "why", strings.Join(why, "; "))
			}
			if err := ping(ctx, ok); err != nil && ctx.Err() == nil {
				b.d.Log.Warn("healthchecks ping failed", "err", err)
			}
		}
	}
}

// ---- Operations for the API and MCP

// ErrNotFound and ErrNotFiring are returned for unknown or finished alert groups.
var (
	ErrNotFound       = errors.New("no such alert group")
	ErrNotFiring      = errors.New("the alert group is not firing")
	ErrBadInput       = errors.New("bad input")
	ErrAckChanged     = errors.New("the alert acknowledgement changed")
	ErrEpisodeChanged = errors.New("the alert incident changed")
)

// Actor is who performs an operation: Name as the audit log and the chat name it (a person, "person /
// agent" for an agent, or a token's name), Via how (api, mcp, console, cli), and Key the principal
// (store.Principal.Key), for checks made later; none for the command line.
type Actor struct {
	Name string
	Via  string
	Key  string
	Cred string // the credential it came with (store.Principal.Cred), for the SSH channel's later checks
}

// ActorOf is the actor a principal is when it comes through via.
func ActorOf(p store.Principal, via string) Actor {
	return Actor{Name: p.Name(), Via: via, Key: p.Key(), Cred: p.Cred()}
}

// GroupView is an alert group as the API shows it.
type GroupView struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	Severity    string            `json:"severity"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Service     string            `json:"service,omitempty"`
	Labels      map[string]string `json:"labels"`
	Alerts      int               `json:"alerts"`
	StartsAt    time.Time         `json:"starts_at"`
	ResolvedAt  *time.Time        `json:"resolved_at,omitempty"`
	Channel     string            `json:"channel"`
	Ack         *store.Ack        `json:"ack,omitempty"`
	AckVersion  string            `json:"-"` // the console form's view of the current acknowledgement
	Episode     string            `json:"-"` // the incident open in the console
	Silence     *store.Silence    `json:"silence,omitempty"`
	Buzzed      *time.Time        `json:"buzzed_at,omitempty"`
	Pushed      *time.Time        `json:"pushover_at,omitempty"`
	Runbook     string            `json:"runbook,omitempty"`
	Dashboard   string            `json:"dashboard,omitempty"`
	Notes       []store.Note      `json:"notes,omitempty"`
	Agents      []store.AgentCall `json:"agents,omitempty"` // the hooks called to wake agents, and how that went
	Page        string            `json:"page,omitempty"`   // the signed link to the alert's page, for people
}

func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (b *Bridge) view(g *store.Group) GroupView {
	firing := 0
	for _, a := range g.Alerts {
		if a.Status == "firing" {
			firing++
		}
	}
	return GroupView{
		ID: g.ID, Status: g.Status, Severity: g.Severity, Title: b.title(g), Description: g.Annotations["description"],
		Service: b.service(g), Labels: g.Labels, Alerts: firing, StartsAt: g.StartsAt, ResolvedAt: tp(g.ResolvedAt),
		Channel: g.Channel, Ack: g.Ack, AckVersion: ackVersion(g), Episode: g.Episode, Silence: g.Silence, Buzzed: tp(g.Buzzed), Pushed: acceptedPushoverAt(g),
		Runbook: b.link(b.d.Config.Links.Runbook, g), Dashboard: b.link(b.d.Config.Links.GrafanaService, g),
		Notes: g.Notes, Agents: g.Agents, Page: b.incidentPage(g),
	}
}

// Groups lists alert groups; with firingOnly, only those firing.
func (b *Bridge) Groups(firingOnly bool) []GroupView {
	var out []GroupView
	for _, g := range b.d.Store.Groups() {
		if firingOnly && !g.Firing() {
			continue
		}
		out = append(out, b.view(&g))
	}
	return out
}

// Group returns one alert group.
func (b *Bridge) Group(id string) (GroupView, error) {
	g, ok := b.d.Store.Group(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	return b.view(&g), nil
}

// Summary is the bridge's state at a glance.
type Summary struct {
	Ready        bool           `json:"ready"`
	NotReady     []string       `json:"not_ready,omitempty"`
	Firing       map[string]int `json:"firing"`
	Acknowledged int            `json:"acknowledged"`
	FeishuCalls  int64          `json:"feishu_api_calls_this_month"`
	FeishuBudget int            `json:"feishu_api_budget"`
	Delivery     DeliverySetup  `json:"delivery"`
}

// DeliverySetup describes configuration, independently of Watchdog and of any delivery attempt.
type DeliverySetup struct {
	CriticalToPeople    bool            `json:"critical_to_people"`
	FeishuApp           bool            `json:"feishu_app"`
	GroupBot            bool            `json:"group_bot"`
	Pushover            bool            `json:"pushover"`
	Bark                bool            `json:"bark"`
	NotificationWebhook bool            `json:"notification_webhook,omitempty"`
	Slack               bool            `json:"slack,omitempty"`
	CriticalChat        string          `json:"critical_chat"`
	CriticalPhone       []string        `json:"critical_phone_escalation,omitempty"`
	CriticalRoutes      []DeliveryRoute `json:"critical_routes,omitempty"`
}

// DeliveryRoute is when a provider is called for a critical alert.
type DeliveryRoute struct {
	Channel string          `json:"channel"`
	Initial string          `json:"initial,omitempty"` // primary or fallback
	After   config.Duration `json:"after,omitempty"`   // escalation for an unacknowledged alert
}

// Delivery reports the configured routes for a new critical alert.
func (b *Bridge) Delivery() DeliverySetup {
	d := DeliverySetup{FeishuApp: b.d.Feishu != nil, GroupBot: b.d.Bot != nil, Pushover: b.d.Pushover != nil,
		NotificationWebhook: b.d.NotificationWebhook != nil,
		Slack:               b.d.Slack != nil,
		Bark:                b.d.Bark != nil, CriticalChat: "none"}
	pol := b.d.Config.Policy("critical")
	chatSelected := pol.Channel == "app" || pol.Channel == "webhook"
	switch {
	case pol.Channel == "app" && d.FeishuApp:
		d.CriticalChat = "app"
		d.CriticalRoutes = append(d.CriticalRoutes, DeliveryRoute{Channel: "app", Initial: "primary"})
		if d.GroupBot {
			d.CriticalRoutes = append(d.CriticalRoutes, DeliveryRoute{Channel: "webhook", Initial: "fallback"})
		}
		if pol.Buzz > 0 {
			d.CriticalRoutes = append(d.CriticalRoutes, DeliveryRoute{Channel: "feishu-buzz", After: pol.Buzz})
		}
	case chatSelected && d.GroupBot:
		d.CriticalChat = "webhook"
		d.CriticalRoutes = append(d.CriticalRoutes, DeliveryRoute{Channel: "webhook", Initial: "primary"})
	case pol.Channel == config.NotificationWebhookChannel && d.NotificationWebhook:
		d.CriticalChat = config.NotificationWebhookChannel
		d.CriticalRoutes = append(d.CriticalRoutes, DeliveryRoute{Channel: config.NotificationWebhookChannel, Initial: "primary"})
	case pol.Channel == config.SlackChannel && d.Slack:
		d.CriticalChat = config.SlackChannel
		d.CriticalRoutes = append(d.CriticalRoutes, DeliveryRoute{Channel: config.SlackChannel, Initial: "primary"})
	}
	if pol.Pushover > 0 && d.Pushover {
		d.CriticalPhone = append(d.CriticalPhone, "Pushover")
	}
	if d.Pushover {
		p := DeliveryRoute{Channel: "pushover", After: pol.Pushover}
		switch {
		case pol.InitialPushover || (chatSelected && d.CriticalChat == "none"):
			p.Initial = "primary"
		case chatSelected:
			p.Initial = "fallback"
		}
		if p.Initial != "" || p.After > 0 {
			d.CriticalRoutes = append(d.CriticalRoutes, p)
		}
	}
	if pol.Bark > 0 && d.Bark {
		d.CriticalPhone = append(d.CriticalPhone, "Bark")
		d.CriticalRoutes = append(d.CriticalRoutes, DeliveryRoute{Channel: "bark", After: pol.Bark})
	}
	d.CriticalToPeople = len(d.CriticalRoutes) > 0
	return d
}

// Summarize returns the summary.
func (b *Bridge) Summarize() Summary {
	ok, why := b.Ready()
	s := Summary{Ready: ok, NotReady: why, Firing: map[string]int{}, FeishuCalls: b.d.Store.Counter(b.quotaKey()),
		FeishuBudget: b.d.Config.Feishu.MonthlyBudget, Delivery: b.Delivery()}
	for _, sev := range config.Severities {
		s.Firing[sev] = 0
	}
	for _, g := range b.d.Store.Groups() {
		if g.Firing() {
			s.Firing[g.Severity]++
			if g.Ack.StopsEscalation() {
				s.Acknowledged++
			}
		}
	}
	return s
}

func (b *Bridge) echo(ctx context.Context, text string) {
	if b.d.Bot == nil {
		return
	}
	if err := b.d.Bot.Send(ctx, text); err != nil {
		b.d.Log.Warn("echo to the alert chat failed", "err", err)
	}
}

func (b *Bridge) viaName(via string) string {
	switch via {
	case "feishu":
		return b.t("Feishu")
	case "pushover":
		return "Pushover"
	case "mcp":
		return "MCP"
	case "cli":
		return b.t("the command line")
	case "console":
		return b.t("the console")
	}
	return "API"
}

// Ack acknowledges a firing group for an API or MCP caller.
func (b *Bridge) Ack(ctx context.Context, id string, who Actor, note string) (GroupView, error) {
	return b.acknowledge(ctx, id, who, note, nil)
}

// AckIfUnchanged acknowledges a firing group only if the console form still shows its current owner.
func (b *Bridge) AckIfUnchanged(ctx context.Context, id string, who Actor, note, expected string) (GroupView, error) {
	return b.acknowledge(ctx, id, who, note, &expected)
}

func (b *Bridge) acknowledge(ctx context.Context, id string, who Actor, note string, expected *string) (GroupView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if !g.Firing() {
		return b.view(&g), ErrNotFiring
	}
	if expected != nil && *expected != ackVersion(&g) {
		return b.view(&g), ErrAckChanged
	}
	b.ack(ctx, &g, who, strings.TrimSpace(note))
	recordParticipation(&g, who, "ack", b.now())
	if b.patchAllowed(&g, false) {
		b.patch(ctx, &g)
	}
	if err := b.d.Store.PutGroup(g); err != nil {
		return GroupView{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "ack", Target: g.ID, Detail: note})
	text := b.t("%s acknowledged \"%s\" via %s", who.Name, b.title(&g), b.viaName(who.Via))
	if actorKind(who) == "agent" {
		text = b.t("%s recorded an Agent acknowledgement for \"%s\" via %s", who.Name, b.title(&g), b.viaName(who.Via))
	}
	b.echo(ctx, text+noteSuffix(b, note))
	return b.view(&g), nil
}

func noteSuffix(b *Bridge, note string) string {
	if note == "" {
		return ""
	}
	return "\n" + b.t("Note: %s", note)
}

// Silence silences a firing group for d (at most 24 hours) with a comment.
func (b *Bridge) Silence(ctx context.Context, id string, d time.Duration, who Actor, comment string) (GroupView, error) {
	return b.silenceAlert(ctx, id, d, who, comment, nil)
}

// SilenceIncident silences a group only if the console form still names its current episode.
func (b *Bridge) SilenceIncident(ctx context.Context, id, episode string, d time.Duration, who Actor, comment string) (GroupView, error) {
	return b.silenceAlert(ctx, id, d, who, comment, &episode)
}

func (b *Bridge) silenceAlert(ctx context.Context, id string, d time.Duration, who Actor, comment string, episode *string) (GroupView, error) {
	comment = strings.TrimSpace(comment)
	if d <= 0 || d > maxSilence {
		return GroupView{}, fmt.Errorf("%w: a silence lasts between 1 second and 24 hours", ErrBadInput)
	}
	if comment == "" {
		return GroupView{}, fmt.Errorf("%w: say why in the comment", ErrBadInput)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if episode != nil && g.Episode != *episode {
		return GroupView{}, ErrEpisodeChanged
	}
	if !g.Firing() {
		return b.view(&g), ErrNotFiring
	}
	if err := b.silence(ctx, &g, d, who, comment); err != nil {
		return GroupView{}, err
	}
	recordParticipation(&g, who, "silence", b.now())
	if b.patchAllowed(&g, false) {
		b.patch(ctx, &g)
	}
	if err := b.d.Store.PutGroup(g); err != nil {
		return GroupView{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "silence", Target: g.ID, Detail: d.String() + ": " + comment})
	b.echo(ctx, b.t("%s silenced \"%s\" for %s via %s: %s", who.Name, b.title(&g), b.dur(d), b.viaName(who.Via), comment))
	return b.view(&g), nil
}

// SilenceLabels silences every alert whose labels equal all of labels for d (at most 24 hours) with a
// comment, as `beacon silence create -matchers` does; the router stops delivering what matches.
// A firing group every alert of which matches is marked silenced (and acknowledged, as Silence does),
// so that its card shows it.
func (b *Bridge) SilenceLabels(ctx context.Context, labels map[string]string, d time.Duration, who Actor, comment string) (alertmanager.Silence, error) {
	comment = strings.TrimSpace(comment)
	if d <= 0 || d > maxSilence {
		return alertmanager.Silence{}, fmt.Errorf("%w: a silence lasts between 1 second and 24 hours", ErrBadInput)
	}
	if comment == "" {
		return alertmanager.Silence{}, fmt.Errorf("%w: say why in the comment", ErrBadInput)
	}
	if len(labels) == 0 {
		return alertmanager.Silence{}, fmt.Errorf("%w: a silence needs at least one matcher, such as alertname=ServiceDown", ErrBadInput)
	}
	for k, v := range labels {
		if k == "" || v == "" {
			return alertmanager.Silence{}, fmt.Errorf("%w: a matcher is label=value, such as service=example.com", ErrBadInput)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	id, err := b.d.AM.CreateSilence(ctx, labels, now, now.Add(d), who.Name, comment)
	if err != nil {
		return alertmanager.Silence{}, err
	}
	for _, g := range b.d.Store.Groups() {
		if !g.Firing() || !everyAlertHas(&g, labels) {
			continue
		}
		b.markSilence(ctx, &g, store.Silence{ID: id, By: who.Name, Until: now.Add(d)}, who)
		recordParticipation(&g, who, "silence", now)
		if b.patchAllowed(&g, false) {
			b.patch(ctx, &g)
		}
		_ = b.d.Store.PutGroup(g)
	}
	s := alertmanager.Silence{ID: id, StartsAt: now, EndsAt: now.Add(d), CreatedBy: who.Name, Comment: comment}
	keys := slices.Sorted(maps.Keys(labels))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		s.Matchers = append(s.Matchers, alertmanager.Matcher{Name: k, Value: labels[k], IsEqual: true})
		parts = append(parts, k+"="+labels[k])
	}
	s.Status.State = "active"
	matchers := strings.Join(parts, ",")
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "silence", Target: matchers, Detail: d.String() + ": " + comment})
	b.echo(ctx, b.t("%s silenced \"%s\" for %s via %s: %s", who.Name, matchers, b.dur(d), b.viaName(who.Via), comment))
	return s, nil
}

// everyAlertHas reports whether each firing alert of the group carries all of labels; a group without
// alerts of its own is judged by its common labels.
func everyAlertHas(g *store.Group, labels map[string]string) bool {
	has := func(l map[string]string) bool {
		for k, v := range labels {
			if l[k] != v {
				return false
			}
		}
		return true
	}
	seen := false
	for _, a := range g.Alerts {
		if a.Status != "firing" {
			continue
		}
		seen = true
		if !has(a.Labels) {
			return false
		}
	}
	if !seen {
		return has(g.Common)
	}
	return true
}

// ExpireSilence ends a silence.
func (b *Bridge) ExpireSilence(ctx context.Context, silenceID string, who Actor) error {
	if silenceID == "" {
		return fmt.Errorf("%w: no silence id", ErrBadInput)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.d.AM.ExpireSilence(ctx, silenceID); err != nil {
		return err
	}
	for _, g := range b.d.Store.Groups() {
		if g.Silence != nil && g.Silence.ID == silenceID {
			g.Silence = nil
			if b.patchAllowed(&g, false) {
				b.patch(ctx, &g)
			}
			_ = b.d.Store.PutGroup(g)
		}
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "expire_silence", Target: silenceID})
	b.echo(ctx, b.t("%s ended silence %s via %s", who.Name, silenceID, b.viaName(who.Via)))
	return nil
}

// Silences lists the active and pending silences.
func (b *Bridge) Silences(ctx context.Context) ([]alertmanager.Silence, error) {
	ss, err := b.d.AM.Silences(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].EndsAt.Before(ss[j].EndsAt) })
	return ss, nil
}

// Audit returns the latest audit entries.
func (b *Bridge) Audit(limit int) []store.AuditEntry { return b.d.Store.AuditLog(limit) }

// AuditQuery is the audit log with a filter and pagination: the entries matching f, newest first,
// and how many match in all.
func (b *Bridge) AuditQuery(f store.AuditFilter) ([]store.AuditEntry, int, error) {
	return b.d.Store.AuditQuery(f)
}
