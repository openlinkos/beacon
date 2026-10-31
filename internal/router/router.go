// Package router does what Alertmanager did, inside the service: it takes alerts from the rule engine
// (vmalert or Prometheus, Alertmanager's API v2: POST /api/v2/alerts), groups them, mutes alerts that a
// firing alert inhibits or that a silence matches, and hands each group to the bridge the way
// Alertmanager's webhook did. One process then owns alerts, silences, cards and acknowledgements.
//
// An alert is firing until its endsAt (the rule engine resends firing alerts every evaluation and sends
// a past endsAt when one resolves; an alert without endsAt lasts resolve_timeout). A group is sent
// group_wait after it first fires, then whenever its alerts change, at most every group_interval; a
// group whose alerts all resolved is sent once more as resolved and forgotten. The Watchdog alert is not
// grouped: it only proves that the rules pipeline works.
//
// After a restart the router knows nothing of alerts yet; Restore gives it the groups that were firing,
// so that an alert that resolved while the service was down is resolved once resolve_timeout passes
// without the rule engine resending it.
package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/status/internal/alertmanager"
	"github.com/openlinkos/status/internal/store"
)

// Config is how alerts are grouped and inhibited.
type Config struct {
	GroupBy        []string
	GroupWait      time.Duration
	GroupInterval  time.Duration
	ResolveTimeout time.Duration
	Routes         []Route
	Inhibit        []Inhibit
}

// Route changes the grouping of the alerts it matches; the first matching route wins.
type Route struct {
	Match   Labels
	GroupBy []string
}

// Inhibit mutes target alerts while a source alert fires with the same values of the Equal labels.
type Inhibit struct {
	Source, Target Labels
	Equal          []string
}

// Labels match labels by regular expressions over the whole value (a missing label is "").
type Labels map[string]*regexp.Regexp

// CompileLabels turns name: regexp pairs into Labels.
func CompileLabels(m map[string]string) (Labels, error) {
	out := Labels{}
	for k, v := range m {
		re, err := regexp.Compile("^(?:" + v + ")$")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = re
	}
	return out, nil
}

func (l Labels) match(labels map[string]string) bool {
	for k, re := range l {
		if !re.MatchString(labels[k]) {
			return false
		}
	}
	return true
}

// Sink takes the groups: the bridge.
type Sink interface {
	HandleWebhook(ctx context.Context, w alertmanager.Webhook) error
	Watchdog()
}

// Postable is an alert as the rule engine posts it.
type Postable struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
}

type alert struct {
	fp                  string
	labels, annotations map[string]string
	startsAt, endsAt    time.Time
	generatorURL        string
}

type group struct {
	key       string
	labels    map[string]string
	firstSeen time.Time
	lastSent  time.Time
	sentHash  string
	notified  bool      // a firing notification was delivered
	retryAt   time.Time // after a failed delivery, not before this
}

// retryAfter is the wait after a failed delivery.
const retryAfter = 30 * time.Second

// Router groups alerts and keeps silences.
type Router struct {
	cfg   Config
	store *store.Store
	sink  Sink
	now   func() time.Time
	log   *slog.Logger

	mu     sync.Mutex
	alerts map[string]*alert
	groups map[string]*group
}

// New creates a router. now and log may be nil.
func New(cfg Config, st *store.Store, sink Sink, now func() time.Time, log *slog.Logger) *Router {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cfg.GroupInterval <= 0 {
		cfg.GroupInterval = 2 * time.Minute
	}
	if cfg.ResolveTimeout <= 0 {
		cfg.ResolveTimeout = 5 * time.Minute
	}
	return &Router{cfg: cfg, store: st, sink: sink, now: now, log: log, alerts: map[string]*alert{}, groups: map[string]*group{}}
}

// Fingerprint identifies an alert by its labels.
func Fingerprint(labels map[string]string) string {
	keys := slices.Sorted(maps.Keys(labels))
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "\x00" + labels[k] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// MaxAlerts is the most alerts one request may carry.
const MaxAlerts = 5000

// Ingest takes posted alerts.
func (r *Router) Ingest(in []Postable) error {
	if len(in) > MaxAlerts {
		return fmt.Errorf("at most %d alerts per request", MaxAlerts)
	}
	now := r.now()
	watchdog := false
	r.mu.Lock()
	for _, p := range in {
		if len(p.Labels) == 0 || p.Labels["alertname"] == "" {
			continue
		}
		if p.Labels["alertname"] == "Watchdog" {
			watchdog = watchdog || p.EndsAt.IsZero() || p.EndsAt.After(now)
			continue
		}
		fp := Fingerprint(p.Labels)
		a := r.alerts[fp]
		if a == nil {
			a = &alert{fp: fp, startsAt: p.StartsAt}
			r.alerts[fp] = a
		}
		if a.startsAt.IsZero() || (!p.StartsAt.IsZero() && p.StartsAt.Before(a.startsAt) && a.endsAt.After(now)) {
			a.startsAt = p.StartsAt
		}
		if a.startsAt.IsZero() {
			a.startsAt = now
		}
		a.labels, a.annotations, a.generatorURL = p.Labels, p.Annotations, p.GeneratorURL
		a.endsAt = p.EndsAt
		if a.endsAt.IsZero() {
			a.endsAt = now.Add(r.cfg.ResolveTimeout)
		}
		if key, labels := r.groupOf(a.labels); r.groups[key] == nil && now.Before(a.endsAt) {
			r.groups[key] = &group{key: key, labels: labels, firstSeen: now} // group_wait counts from here
		}
	}
	r.mu.Unlock()
	if watchdog && r.sink != nil {
		r.sink.Watchdog()
	}
	return nil
}

// Restore takes the groups the bridge had firing before a restart.
func (r *Router) Restore(groups []store.Group) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range groups {
		if !g.Firing() {
			continue
		}
		var members []*alert
		for _, sa := range g.Alerts {
			if sa.Status != "firing" || len(sa.Labels) == 0 {
				continue
			}
			a := &alert{fp: Fingerprint(sa.Labels), labels: sa.Labels, annotations: sa.Annotations, startsAt: sa.StartsAt,
				endsAt: now.Add(r.cfg.ResolveTimeout), generatorURL: sa.GeneratorURL}
			r.alerts[a.fp] = a
			members = append(members, a)
		}
		if len(members) == 0 {
			continue
		}
		key, labels := r.groupOf(members[0].labels)
		r.groups[key] = &group{key: key, labels: labels, firstSeen: g.StartsAt, lastSent: now, notified: true,
			sentHash: hashOf(members, map[string]bool{}, now)}
	}
}

// groupOf is the group key and group labels of an alert.
func (r *Router) groupOf(labels map[string]string) (string, map[string]string) {
	by, route := r.cfg.GroupBy, -1
	for i, rt := range r.cfg.Routes {
		if rt.Match.match(labels) {
			route = i
			if len(rt.GroupBy) > 0 {
				by = rt.GroupBy
			}
			break
		}
	}
	gl := map[string]string{}
	parts := make([]string, 0, len(by))
	for _, k := range slices.Sorted(slices.Values(by)) {
		if v, ok := labels[k]; ok {
			gl[k] = v
			parts = append(parts, fmt.Sprintf("%s=%q", k, v))
		}
	}
	prefix := "{}"
	if route >= 0 {
		prefix = fmt.Sprintf("{}/{route=%d}", route)
	}
	return prefix + ":{" + strings.Join(parts, ",") + "}", gl
}

// muted reports whether a firing alert is inhibited by another firing alert or matched by an active silence.
func (r *Router) muted(a *alert, firing []*alert, silences []store.SilenceRecord) bool {
	for _, s := range silences {
		if silenceMatches(s, a.labels) {
			return true
		}
	}
	for _, in := range r.cfg.Inhibit {
		if !in.Target.match(a.labels) {
			continue
		}
		for _, src := range firing {
			if src.fp == a.fp || !in.Source.match(src.labels) {
				continue
			}
			same := true
			for _, k := range in.Equal {
				if src.labels[k] != a.labels[k] {
					same = false
					break
				}
			}
			if same {
				return true
			}
		}
	}
	return false
}

func silenceMatches(s store.SilenceRecord, labels map[string]string) bool {
	for _, m := range s.Matchers {
		v := labels[m.Name]
		var ok bool
		if m.IsRegex {
			re, err := regexp.Compile("^(?:" + m.Value + ")$")
			ok = err == nil && re.MatchString(v)
		} else {
			ok = v == m.Value
		}
		if ok != m.IsEqual {
			return false
		}
	}
	return len(s.Matchers) > 0
}

func hashOf(members []*alert, muted map[string]bool, now time.Time) string {
	parts := make([]string, 0, len(members))
	for _, a := range members {
		st := "resolved"
		if now.Before(a.endsAt) && !muted[a.fp] {
			st = "firing"
		}
		parts = append(parts, a.fp+":"+st)
	}
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:8])
}

// Flush sends the groups that are due. Run calls it every few seconds.
func (r *Router) Flush(ctx context.Context) {
	now := r.now()
	silences := r.activeSilences(now)
	type send struct {
		g    *group
		hook alertmanager.Webhook
		hash string
		done bool // resolved: forget the group once delivered
	}
	var due []send

	r.mu.Lock()
	var firing []*alert
	for _, a := range r.alerts {
		if now.Before(a.endsAt) {
			firing = append(firing, a)
		}
	}
	muted := map[string]bool{}
	for _, a := range firing {
		if r.muted(a, firing, silences) {
			muted[a.fp] = true
		}
	}
	members := map[string][]*alert{}
	for _, a := range r.alerts {
		key, labels := r.groupOf(a.labels)
		members[key] = append(members[key], a)
		if r.groups[key] == nil {
			r.groups[key] = &group{key: key, labels: labels, firstSeen: now}
		}
	}
	for key, g := range r.groups {
		ms := members[key]
		sort.Slice(ms, func(i, j int) bool { return ms[i].fp < ms[j].fp })
		live := false // a firing alert that is not muted
		for _, a := range ms {
			live = live || (now.Before(a.endsAt) && !muted[a.fp])
		}
		h := hashOf(ms, muted, now)
		switch {
		case now.Before(g.retryAt):
			continue
		case live && !g.notified && now.Sub(g.firstSeen) < r.cfg.GroupWait:
			continue // wait for more alerts of the group
		case live && (!g.notified || (h != g.sentHash && now.Sub(g.lastSent) >= r.cfg.GroupInterval)):
			due = append(due, send{g: g, hook: r.webhook(g, ms, muted, now, "firing"), hash: h})
		case !live && g.notified && !anyFiring(ms, now):
			due = append(due, send{g: g, hook: r.webhook(g, ms, muted, now, "resolved"), hash: h, done: true})
		case !live && !g.notified && !anyFiring(ms, now):
			r.forget(g, ms) // fired and resolved (or stayed muted) before it was ever sent
		}
	}
	r.mu.Unlock()

	for _, d := range due {
		err := r.sink.HandleWebhook(ctx, d.hook)
		r.mu.Lock()
		if err != nil {
			d.g.retryAt = now.Add(retryAfter)
			r.log.Error("delivering an alert group failed", "group", d.g.key, "err", err)
		} else {
			d.g.lastSent, d.g.sentHash, d.g.notified = now, d.hash, true
			if d.done {
				r.forget(d.g, members[d.g.key])
			}
		}
		r.mu.Unlock()
	}
}

func anyFiring(ms []*alert, now time.Time) bool {
	for _, a := range ms {
		if now.Before(a.endsAt) {
			return true
		}
	}
	return false
}

// forget drops a group and its resolved alerts (with r.mu held).
func (r *Router) forget(g *group, ms []*alert) {
	now := r.now()
	for _, a := range ms {
		if !now.Before(a.endsAt) {
			delete(r.alerts, a.fp)
		}
	}
	delete(r.groups, g.key)
}

// webhook builds the payload Alertmanager would have sent: the firing alerts that are not muted, or,
// for a resolved group, its alerts as resolved.
func (r *Router) webhook(g *group, ms []*alert, muted map[string]bool, now time.Time, status string) alertmanager.Webhook {
	w := alertmanager.Webhook{Version: "4", GroupKey: g.key, Status: status, Receiver: "linkos-status", GroupLabels: maps.Clone(g.labels)}
	for _, a := range ms {
		st := "resolved"
		if now.Before(a.endsAt) {
			if muted[a.fp] {
				continue
			}
			st = "firing"
		}
		if status == "firing" && st == "resolved" && !g.notified {
			continue
		}
		w.Alerts = append(w.Alerts, alertmanager.Alert{Status: st, Labels: a.labels, Annotations: a.annotations, StartsAt: a.startsAt,
			EndsAt: a.endsAt, GeneratorURL: a.generatorURL, Fingerprint: a.fp})
	}
	w.CommonLabels = common(w.Alerts, func(a alertmanager.Alert) map[string]string { return a.Labels })
	w.CommonAnnotations = common(w.Alerts, func(a alertmanager.Alert) map[string]string { return a.Annotations })
	return w
}

func common(as []alertmanager.Alert, get func(alertmanager.Alert) map[string]string) map[string]string {
	if len(as) == 0 {
		return map[string]string{}
	}
	out := maps.Clone(get(as[0]))
	for _, a := range as[1:] {
		m := get(a)
		for k, v := range out {
			if m[k] != v {
				delete(out, k)
			}
		}
	}
	return out
}

// Run flushes every five seconds until ctx ends.
func (r *Router) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Flush(ctx)
		}
	}
}

// Firing is the number of firing alerts the router holds (for metrics).
func (r *Router) Firing() int {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.alerts {
		if now.Before(a.endsAt) {
			n++
		}
	}
	return n
}

// ---- Silences (the bridge's Silencer)

func (r *Router) activeSilences(now time.Time) []store.SilenceRecord {
	var out []store.SilenceRecord
	for _, s := range r.store.SilenceRecords() {
		if !now.Before(s.StartsAt) && now.Before(s.EndsAt) {
			out = append(out, s)
		}
	}
	return out
}

// CreateSilence silences alerts whose labels equal all of labels, from start to end.
func (r *Router) CreateSilence(_ context.Context, labels map[string]string, start, end time.Time, createdBy, comment string) (string, error) {
	if len(labels) == 0 {
		return "", errors.New("a silence needs at least one label")
	}
	if !end.After(start) {
		return "", errors.New("a silence must end after it starts")
	}
	ms := make([]store.Matcher, 0, len(labels))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		ms = append(ms, store.Matcher{Name: k, Value: labels[k], IsEqual: true})
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	err := r.store.PutSilence(store.SilenceRecord{ID: id, Matchers: ms, StartsAt: start, EndsAt: end, CreatedBy: createdBy, Comment: comment})
	return id, err
}

// ExpireSilence ends a silence now.
func (r *Router) ExpireSilence(_ context.Context, id string) error {
	now := r.now()
	for _, s := range r.store.SilenceRecords() {
		if s.ID != id {
			continue
		}
		if !now.Before(s.EndsAt) {
			return errors.New("the silence has ended already")
		}
		if now.Before(s.StartsAt) {
			s.StartsAt = now
		}
		s.EndsAt = now
		return r.store.PutSilence(s)
	}
	return store.ErrNotFound
}

// Silences lists the silences in Alertmanager's form.
func (r *Router) Silences(context.Context) ([]alertmanager.Silence, error) {
	now := r.now()
	var out []alertmanager.Silence
	for _, s := range r.store.SilenceRecords() {
		x := alertmanager.Silence{ID: s.ID, StartsAt: s.StartsAt, EndsAt: s.EndsAt, CreatedBy: s.CreatedBy, Comment: s.Comment}
		for _, m := range s.Matchers {
			x.Matchers = append(x.Matchers, alertmanager.Matcher{Name: m.Name, Value: m.Value, IsRegex: m.IsRegex, IsEqual: m.IsEqual})
		}
		switch {
		case now.Before(s.StartsAt):
			x.Status.State = "pending"
		case now.Before(s.EndsAt):
			x.Status.State = "active"
		default:
			x.Status.State = "expired"
		}
		out = append(out, x)
	}
	return out, nil
}
