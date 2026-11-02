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

// Deps are the bridge's collaborators. Feishu, Pushover, Bark, Bot and VM may be nil.
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	AM       Silencer
	Feishu   feishu.API
	Pushover Pusher
	Bark     Barker
	Bot      Poster
	VM       Querier
	Catalog  func() *catalog.Catalog
	Metrics  *metrics.Metrics
	Log      *slog.Logger
	Now      func() time.Time
	// WSReady reports whether Feishu's long connection is up; nil when callbacks come over HTTP.
	WSReady func() bool
	// ProbeNow is the board of requests to probe on demand (probenow.go); nil without a gateway.
	ProbeNow *probenow.Board
	// Pages builds the signed links to the pages people open from the chat (pages.go); nil: no links.
	Pages *link.Pages
}

// Bridge is the alert bridge.
type Bridge struct {
	d       Deps
	lang    i18n.Lang
	started time.Time

	mu         sync.Mutex // serialises every change to the groups
	watchdogAt atomic.Int64
	feishuFail atomic.Int64 // unix nanoseconds of the last failed Feishu call, 0 after a success
	hooks      hooks        // calls that wake agents, in flight (agents.go)
}

const (
	patchEvery   = 5 * time.Minute
	receiptEvery = 30 * time.Second
	keepResolved = 7 * 24 * time.Hour
	watchdogMax  = 3 * time.Minute
	maxSilence   = 24 * time.Hour
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
	if d.Metrics != nil {
		d.Metrics.Register("linkos_beacon_bridge_ready", "1 when the bridge can deliver alerts", "gauge", func() []metrics.Sample {
			ok, _ := b.Ready()
			return []metrics.Sample{{Value: map[bool]float64{true: 1, false: 0}[ok]}}
		})
		d.Metrics.Register("linkos_beacon_feishu_api_calls", "Feishu app API calls this month", "gauge", func() []metrics.Sample {
			return []metrics.Sample{{Value: float64(b.d.Store.Counter(b.quotaKey()))}}
		})
		d.Metrics.Register("linkos_beacon_alert_groups_firing", "Alert groups firing, by severity", "gauge", func() []metrics.Sample {
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
		b.d.Metrics.Inc("linkos_beacon_notifications_total", "Notifications sent, by channel and result", metrics.Labels("channel", channel, "result", result))
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
			g.Buzzed, g.Pushed, g.Barked, g.ReceiptAt, g.ResolvedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
			g.Receipt, g.MessageID, g.CardHash, g.Channel = "", "", "", ""
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
		b.wake(&g, "resolved")
	}
	return b.d.Store.PutGroup(g)
}

func (b *Bridge) notifyNew(ctx context.Context, g *store.Group) {
	g.NotifiedAt = b.now()
	pol := b.d.Config.Policy(g.Severity)
	if pol.Channel == "app" && b.appAllowed(g.Severity) {
		id, err := b.d.Feishu.SendCard(ctx, b.d.Config.Feishu.ChatID, b.card(ctx, g), g.ID+"-"+g.StartsAt.UTC().Format("20060102T150405"))
		b.feishuResult(err)
		if err == nil {
			g.Channel, g.MessageID, g.CardAt, g.CardHash = "app", id, b.now(), cardHash(g)
			b.count("feishu", "ok")
			return
		}
		b.count("feishu", "error")
		b.d.Log.Error("feishu card failed; falling back", "group", g.ID, "err", err)
		if g.Severity == "critical" && b.d.Pushover != nil {
			b.push(ctx, g)
		}
	}
	if b.d.Bot != nil {
		err := b.d.Bot.Send(ctx, b.plainFiring(g))
		if err == nil {
			g.Channel = "webhook"
			b.count("feishu-bot", "ok")
			return
		}
		b.count("feishu-bot", "error")
		b.d.Log.Error("feishu group bot failed", "group", g.ID, "err", err)
	}
	if g.Pushed.IsZero() && b.d.Pushover != nil && g.Severity == "critical" {
		b.push(ctx, g)
	}
	if g.Channel == "" {
		g.Channel = "none"
		if b.d.Feishu == nil && b.d.Bot == nil && b.d.Pushover == nil {
			b.d.Log.Info("alert recorded; no notification channel is configured", "group", g.ID, "summary", b.title(g))
			return
		}
		b.d.Log.Error("alert could not be delivered by any channel", "group", g.ID, "summary", b.title(g))
	}
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
	err := b.d.Feishu.PatchCard(ctx, g.MessageID, b.card(ctx, g))
	b.feishuResult(err)
	if err != nil {
		b.count("feishu-patch", "error")
		b.d.Log.Warn("feishu card update failed", "group", g.ID, "err", err)
		return
	}
	b.count("feishu-patch", "ok")
	g.CardAt, g.CardHash = b.now(), cardHash(g)
}

func (b *Bridge) resolve(ctx context.Context, g *store.Group) {
	if !g.Pushed.IsZero() && b.d.Pushover != nil && g.Ack == nil {
		if err := b.d.Pushover.CancelByTag(ctx, g.ID); err != nil {
			b.d.Log.Warn("pushover cancel failed", "group", g.ID, "err", err)
		}
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
	rc, err := b.d.Pushover.Send(ctx, notify.Emergency{
		Title: b.t("%s: %s", b.severityName(g.Severity), b.title(g)), Message: b.plainBody(g),
		URL: b.link(b.d.Config.Links.Runbook, g), URLTitle: b.t("Runbook"), Tag: g.ID,
	})
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
	if !g.Firing() {
		return feishu.Response{Toast: b.t("This alert has already resolved"), ToastKind: "info", Card: b.card(ctx, &g)}
	}
	var toast string
	switch act {
	case "ack", "note":
		note := ""
		if act == "note" {
			note, _ = a.Form["note"].(string)
			note = strings.TrimSpace(note)
		}
		b.ack(ctx, &g, person.Name, "feishu", note)
		toast = b.t("Acknowledged")
	case "silence":
		d, err := time.ParseDuration(fmt.Sprint(a.Value["d"]))
		if err != nil || d <= 0 || d > maxSilence {
			return feishu.Response{Toast: b.t("Unknown action"), ToastKind: "error"}
		}
		if err := b.silence(ctx, &g, d, person.Name, "feishu", "from the Feishu card"); err != nil {
			b.d.Log.Error("silence failed", "group", g.ID, "err", err)
			return feishu.Response{Toast: b.t("Could not silence: %s", err.Error()), ToastKind: "error"}
		}
		toast = b.t("Silenced for %s", b.dur(d))
	default:
		return feishu.Response{Toast: b.t("Unknown action"), ToastKind: "error"}
	}
	card := b.card(ctx, &g)
	g.CardAt, g.CardHash = b.now(), cardHash(&g)
	if err := b.d.Store.PutGroup(g); err != nil {
		b.d.Log.Error("save group", "err", err)
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: person.Name, Via: "feishu", Action: act, Target: g.ID, Detail: b.title(&g)})
	return feishu.Response{Toast: toast, ToastKind: "success", Card: card}
}

func (b *Bridge) ack(ctx context.Context, g *store.Group, by, via, note string) {
	g.Ack = &store.Ack{By: by, Via: via, Note: note, At: b.now()}
	if !g.Pushed.IsZero() && b.d.Pushover != nil {
		if err := b.d.Pushover.CancelByTag(ctx, g.ID); err != nil {
			b.d.Log.Warn("pushover cancel failed", "group", g.ID, "err", err)
		}
	}
}

func (b *Bridge) silence(ctx context.Context, g *store.Group, d time.Duration, by, via, comment string) error {
	labels := g.Labels
	if len(labels) == 0 {
		labels = map[string]string{"alertname": g.Common["alertname"]}
	}
	now := b.now()
	id, err := b.d.AM.CreateSilence(ctx, labels, now, now.Add(d), by, comment)
	if err != nil {
		return err
	}
	g.Silence = &store.Silence{ID: id, By: by, Until: now.Add(d)}
	if g.Ack == nil {
		b.ack(ctx, g, by, via, "")
	}
	return nil
}

// ---- Escalation and upkeep

// Tick escalates unacknowledged alerts, polls Pushover receipts, restarts escalation when an
// acknowledgement or silence lapses, and drops old resolved groups. Run calls it every 15 seconds.
func (b *Bridge) Tick(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	ttl := b.d.Config.AckTTL.D()
	for _, g := range b.d.Store.Groups() {
		if !g.Firing() {
			continue
		}
		changed := false
		pol := b.d.Config.Policy(g.Severity)
		if g.Ack != nil && now.Sub(g.Ack.At) > ttl && (g.Silence == nil || now.After(g.Silence.Until)) {
			b.d.Log.Info("acknowledgement lapsed; escalating again", "group", g.ID)
			g.Ack, g.Silence = nil, nil
			g.NotifiedAt, g.Buzzed, g.Pushed, g.Barked, g.Receipt = now, time.Time{}, time.Time{}, time.Time{}, ""
			if b.patchAllowed(&g, false) {
				b.patch(ctx, &g)
			}
			changed = true
		}
		if g.Ack == nil && !g.NotifiedAt.IsZero() {
			since := now.Sub(g.NotifiedAt)
			if pol.Buzz > 0 && g.Buzzed.IsZero() && g.MessageID != "" && b.d.Feishu != nil && since >= pol.Buzz.D() && b.quota() < 1 {
				err := b.d.Feishu.UrgentApp(ctx, g.MessageID, b.onCallIDs())
				b.feishuResult(err)
				if err != nil {
					b.count("feishu-buzz", "error")
					b.d.Log.Warn("feishu buzz failed", "group", g.ID, "err", err)
				} else {
					b.count("feishu-buzz", "ok")
				}
				g.Buzzed, changed = now, true
				if b.patchAllowed(&g, false) {
					b.patch(ctx, &g)
				}
			}
			if pol.Pushover > 0 && g.Pushed.IsZero() && b.d.Pushover != nil && since >= pol.Pushover.D() {
				b.push(ctx, &g)
				if g.Pushed.IsZero() {
					g.Pushed = now // do not retry every tick; the failure is counted and logged
				}
				changed = true
			}
			if pol.Bark > 0 && g.Barked.IsZero() && b.d.Bark != nil && since >= pol.Bark.D() {
				if err := b.d.Bark.Send(ctx, b.t("%s: %s", b.severityName(g.Severity), b.title(&g)), b.plainBody(&g), b.link(b.d.Config.Links.Runbook, &g)); err != nil {
					b.count("bark", "error")
					b.d.Log.Warn("bark failed", "group", g.ID, "err", err)
				} else {
					b.count("bark", "ok")
				}
				g.Barked, changed = now, true
			}
		}
		if g.Ack == nil && g.Receipt != "" && b.d.Pushover != nil && now.Sub(g.ReceiptAt) >= receiptEvery {
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
				g.Ack = &store.Ack{By: by, Via: "pushover", At: r.At}
				if g.Ack.At.IsZero() {
					g.Ack.At = now
				}
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
		}
	}
	b.d.Store.DeleteGroups(func(g store.Group) bool { return !g.Firing() && now.Sub(g.ResolvedAt) > keepResolved })
}

func (b *Bridge) onCallIDs() []string {
	ids := make([]string, 0, len(b.d.Config.Feishu.OnCall))
	for _, p := range b.d.Config.Feishu.OnCall {
		ids = append(ids, p.OpenID)
	}
	return ids
}

// Ready reports whether alerts can reach people, with the reasons when they cannot.
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
	return len(why) == 0, why
}

// Run ticks until ctx is done and pings the healthchecks.io check every minute when ready.
func (b *Bridge) Run(ctx context.Context, ping func(ctx context.Context, ok bool) error) {
	b.resumeWakes(ctx)
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	beat := time.NewTicker(time.Minute)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			b.Tick(ctx)
		case <-beat.C:
			if ping == nil {
				continue
			}
			ok, why := b.Ready()
			if !ok {
				b.d.Log.Warn("bridge not ready", "why", strings.Join(why, "; "))
			}
			if err := ping(ctx, ok); err != nil {
				b.d.Log.Warn("healthchecks ping failed", "err", err)
			}
		}
	}
}

// ---- Operations for the API and MCP

// ErrNotFound and ErrNotFiring are returned for unknown or finished alert groups.
var (
	ErrNotFound  = errors.New("no such alert group")
	ErrNotFiring = errors.New("the alert group is not firing")
	ErrBadInput  = errors.New("bad input")
)

// Actor is who performs an operation through the API or MCP.
type Actor struct {
	Name string // token name
	Via  string // api or mcp
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
		Channel: g.Channel, Ack: g.Ack, Silence: g.Silence, Buzzed: tp(g.Buzzed), Pushed: tp(g.Pushed),
		Runbook: b.link(b.d.Config.Links.Runbook, g), Dashboard: b.link(b.d.Config.Links.GrafanaService, g),
		Notes: g.Notes, Agents: g.Agents, Page: b.AlertPage(g.ID),
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
}

// Summarize returns the summary.
func (b *Bridge) Summarize() Summary {
	ok, why := b.Ready()
	s := Summary{Ready: ok, NotReady: why, Firing: map[string]int{}, FeishuCalls: b.d.Store.Counter(b.quotaKey()), FeishuBudget: b.d.Config.Feishu.MonthlyBudget}
	for _, sev := range config.Severities {
		s.Firing[sev] = 0
	}
	for _, g := range b.d.Store.Groups() {
		if g.Firing() {
			s.Firing[g.Severity]++
			if g.Ack != nil {
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
	}
	return "API"
}

// Ack acknowledges a firing group for an API or MCP caller.
func (b *Bridge) Ack(ctx context.Context, id string, who Actor, note string) (GroupView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if !g.Firing() {
		return b.view(&g), ErrNotFiring
	}
	b.ack(ctx, &g, who.Name, who.Via, strings.TrimSpace(note))
	if b.patchAllowed(&g, false) {
		b.patch(ctx, &g)
	}
	if err := b.d.Store.PutGroup(g); err != nil {
		return GroupView{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "ack", Target: g.ID, Detail: note})
	b.echo(ctx, b.t("%s acknowledged \"%s\" via %s", who.Name, b.title(&g), b.viaName(who.Via))+noteSuffix(b, note))
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
	if !g.Firing() {
		return b.view(&g), ErrNotFiring
	}
	if err := b.silence(ctx, &g, d, who.Name, who.Via, comment); err != nil {
		return GroupView{}, err
	}
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
