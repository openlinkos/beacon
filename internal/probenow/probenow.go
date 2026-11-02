// Package probenow is probing on demand: an agent asks the centre to have a target probed again at
// once, every probe location that has the target assigned is woken through the gateway's long-poll
// route, probes it with its usual modules and settings and posts what it found, and the agent sees each
// location's answer within a minute instead of waiting for the next cycle and the rule engine.
//
// The board holds the requests in memory: each carries the targets (as the probes understand them) and
// the locations expected to answer, lives two minutes and is lost at a restart, which is fine. Limits
// protect the probes: one request per target every 15 seconds, and three requests in flight per token.
package probenow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/targets"
)

// Deps are what the board needs: the catalogue and the target sets (to know which locations probe a
// target), and the metrics registry. Metrics and Now may be nil.
type Deps struct {
	Catalog func() *catalog.Catalog
	Targets func() *targets.Set
	Metrics *metrics.Metrics
	Now     func() time.Time
}

// Limits.
const (
	TTL        = 2 * time.Minute  // a request is dropped after this
	MaxTargets = 20               // targets in one request
	PerToken   = 3                // requests in flight per token
	PerTarget  = 15 * time.Second // between two requests for one target
)

// Errors. The messages carry the detail; callers match with errors.Is.
var (
	ErrNotFound = errors.New("not found")
	ErrBusy     = errors.New("busy")
	ErrTooMany  = errors.New("too many targets")
)

// Want names what to probe: a target (with a module, or in every module it has), or every target of a
// service.
type Want struct {
	Target, Module, Service string
}

// Target is one target of a request and the locations expected to probe it.
type Target struct {
	Instance  string   `json:"instance"`
	Module    string   `json:"module"`
	Service   string   `json:"service,omitempty"`
	Locations []string `json:"locations"`

	labels map[string]string
}

// Result is one location's answer for one target, and when it reached the gateway.
type Result struct {
	Location string
	targets.TargetResult
	Received time.Time
}

// View is a request as callers read it. Missing are the expected locations that have not answered.
type View struct {
	ID       string
	By       string
	Created  time.Time
	Expires  time.Time
	Targets  []Target
	Expected []string
	Results  []Result
	Missing  []string
	Done     bool
}

type request struct {
	View
	answered map[string]time.Time
	settled  bool // the caller stopped waiting; it no longer counts as in flight
	done     chan struct{}
}

// Board holds the requests.
type Board struct {
	d Deps

	mu      sync.Mutex
	reqs    map[string]*request
	last    map[string]time.Time       // by target instance: when it was last requested
	waiters map[string][]chan struct{} // by location: long-polls to wake
}

// New creates a board and registers its metrics.
func New(d Deps) *Board {
	if d.Now == nil {
		d.Now = time.Now
	}
	b := &Board{d: d, reqs: map[string]*request{}, last: map[string]time.Time{}, waiters: map[string][]chan struct{}{}}
	if d.Metrics != nil {
		d.Metrics.Register("linkos_probe_now_requests_open", "Probe-on-demand requests waiting for locations to answer", "gauge", func() []metrics.Sample {
			return []metrics.Sample{{Value: float64(b.Open())}}
		})
	}
	return b
}

func (b *Board) now() time.Time { return b.d.Now() }

func (b *Board) count(name, help, labels string) {
	if b.d.Metrics != nil {
		b.d.Metrics.Inc(name, help, labels)
	}
}

// Open is the number of requests not yet complete, not yet settled and not expired.
func (b *Board) Open() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweep()
	n := 0
	for _, r := range b.reqs {
		if !r.settled && !r.Done {
			n++
		}
	}
	return n
}

// sweep drops expired requests and old target stamps. Called with the lock held.
func (b *Board) sweep() {
	now := b.now()
	for id, r := range b.reqs {
		if !now.Before(r.Expires) {
			delete(b.reqs, id)
		}
	}
	for t, at := range b.last {
		if now.Sub(at) >= PerTarget {
			delete(b.last, t)
		}
	}
}

func labelsKey(m map[string]string) string {
	keys := slices.Sorted(maps.Keys(m))
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k + "=" + m[k] + "\x00")
	}
	return sb.String()
}

// resolve turns wants into targets with the active locations that probe each, from the catalogue and the
// target sets: for every active location, its view of the sets (as the gateway hands it out) is scanned.
func (b *Board) resolve(wants []Want) ([]Target, error) {
	cat, set, now := b.d.Catalog(), b.d.Targets(), b.now()
	ids := make([]string, 0, len(cat.Locations))
	for _, l := range cat.Locations {
		ids = append(ids, l.ID)
	}
	var all []*Target
	index := map[string]*Target{}
	for _, l := range cat.Locations {
		if !cat.Expected(l, now) {
			continue
		}
		for _, g := range set.For(l.ID, l.Region, ids) {
			for _, inst := range g.Targets {
				k := inst + "\x00" + labelsKey(g.Labels)
				t := index[k]
				if t == nil {
					t = &Target{Instance: inst, Module: g.Labels["module"], Service: g.Labels["service"], labels: maps.Clone(g.Labels)}
					index[k] = t
					all = append(all, t)
				}
				if !slices.Contains(t.Locations, l.ID) {
					t.Locations = append(t.Locations, l.ID)
				}
			}
		}
	}
	var out []Target
	for _, w := range wants {
		found := false
		for _, t := range all {
			match := (w.Target != "" && t.Instance == w.Target && (w.Module == "" || t.Module == w.Module)) ||
				(w.Target == "" && w.Service != "" && t.Service == w.Service)
			if !match {
				continue
			}
			found = true
			if !slices.ContainsFunc(out, func(o Target) bool { return o.Instance == t.Instance && labelsKey(o.labels) == labelsKey(t.labels) }) {
				c := *t
				c.Locations = slices.Clone(t.Locations)
				slices.Sort(c.Locations)
				out = append(out, c)
			}
		}
		if !found {
			return nil, b.whyNotFound(set, w)
		}
	}
	if len(out) > MaxTargets {
		return nil, fmt.Errorf("%w: %d targets match; at most %d in one request, so name a target", ErrTooMany, len(out), MaxTargets)
	}
	return out, nil
}

// whyNotFound tells a target no set has from one no active location probes.
func (b *Board) whyNotFound(set *targets.Set, w Want) error {
	inSets := false
	for _, f := range set.Files() {
		for _, g := range set.Groups(f) {
			if (w.Target != "" && slices.Contains(g.Targets, w.Target) && (w.Module == "" || g.Labels["module"] == w.Module)) ||
				(w.Target == "" && g.Labels["service"] == w.Service) {
				inSets = true
			}
		}
	}
	what := "service " + w.Service
	if w.Target != "" {
		what = "target " + w.Target
		if w.Module != "" {
			what += " with module " + w.Module
		}
	}
	if inSets {
		return fmt.Errorf("%w: no active location probes %s", ErrNotFound, what)
	}
	return fmt.Errorf("%w: no target set has %s", ErrNotFound, what)
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Create makes a request for the token by, after resolving wants to targets and the locations that probe
// them, and wakes those locations' long-polls. ErrNotFound when nothing matches or no active location
// probes it; ErrBusy when the token has PerToken requests in flight or a target was requested within
// PerTarget; ErrTooMany over MaxTargets targets.
func (b *Board) Create(by string, wants []Want) (View, error) {
	if len(wants) == 0 {
		return View{}, fmt.Errorf("%w: nothing to probe", ErrNotFound)
	}
	ts, err := b.resolve(wants)
	if err != nil {
		return View{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweep()
	now := b.now()
	inFlight := 0
	for _, r := range b.reqs {
		if r.By == by && !r.settled && !r.Done {
			inFlight++
		}
	}
	if inFlight >= PerToken {
		return View{}, fmt.Errorf("%w: this token has %d requests in flight; wait for one to finish", ErrBusy, inFlight)
	}
	for _, t := range ts {
		if at, ok := b.last[t.Instance]; ok && now.Sub(at) < PerTarget {
			return View{}, fmt.Errorf("%w: %s was probed on demand %s ago; try again in %s", ErrBusy, t.Instance,
				now.Sub(at).Round(time.Second), (PerTarget - now.Sub(at)).Round(time.Second))
		}
	}
	expected := map[string]bool{}
	for _, t := range ts {
		b.last[t.Instance] = now
		for _, l := range t.Locations {
			expected[l] = true
		}
	}
	r := &request{View: View{ID: newID(), By: by, Created: now, Expires: now.Add(TTL), Targets: ts,
		Expected: slices.Sorted(maps.Keys(expected)), Results: []Result{}}, answered: map[string]time.Time{}, done: make(chan struct{})}
	r.Missing = slices.Clone(r.Expected)
	b.reqs[r.ID] = r
	b.count("linkos_probe_now_requests_total", "Probe-on-demand requests made by agents", "")
	for l := range expected {
		for _, ch := range b.waiters[l] {
			close(ch)
		}
		delete(b.waiters, l)
	}
	return r.snapshot(), nil
}

// snapshot copies the view for reading without the lock. Called with the lock held.
func (r *request) snapshot() View {
	v := r.View
	v.Targets = slices.Clone(r.Targets)
	v.Expected = slices.Clone(r.Expected)
	v.Results = slices.Clone(r.Results)
	v.Missing = slices.Clone(r.Missing)
	return v
}

// pending lists the requests location has not answered yet, with the targets it probes. Called with the
// lock held.
func (b *Board) pending(location string) []targets.Request {
	var reqs []*request
	for _, r := range b.reqs {
		if _, done := r.answered[location]; done || !slices.Contains(r.Expected, location) {
			continue
		}
		reqs = append(reqs, r)
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Created.Before(reqs[j].Created) })
	out := make([]targets.Request, 0, len(reqs))
	for _, r := range reqs {
		req := targets.Request{ID: r.ID}
		for _, t := range r.Targets {
			if slices.Contains(t.Locations, location) {
				req.Groups = append(req.Groups, targets.Group{Targets: []string{t.Instance}, Labels: maps.Clone(t.labels)})
			}
		}
		out = append(out, req)
	}
	return out
}

// Wait answers location's long-poll: at once with its pending requests, otherwise when a request for it
// is made, when wait passes (an empty list) or when ctx ends.
func (b *Board) Wait(ctx context.Context, location string, wait time.Duration) []targets.Request {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		b.mu.Lock()
		b.sweep()
		out := b.pending(location)
		if len(out) > 0 || wait <= 0 {
			b.mu.Unlock()
			return out
		}
		ch := make(chan struct{})
		b.waiters[location] = append(b.waiters[location], ch)
		b.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			b.unwait(location, ch)
			return []targets.Request{}
		case <-timer.C:
			b.unwait(location, ch)
			b.mu.Lock()
			out := b.pending(location)
			b.mu.Unlock()
			return out
		}
	}
}

func (b *Board) unwait(location string, ch chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.waiters[location] = slices.DeleteFunc(b.waiters[location], func(c chan struct{}) bool { return c == ch })
	if len(b.waiters[location]) == 0 {
		delete(b.waiters, location)
	}
}

// Answer records what location found for request id. Results are kept only for the request's own
// targets, one per target; a second answer from the same location changes nothing. ErrNotFound when
// the request is unknown or has expired.
func (b *Board) Answer(id, location string, results []targets.TargetResult) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweep()
	r := b.reqs[id]
	if r == nil {
		return fmt.Errorf("%w: no request %s; it may have expired", ErrNotFound, id)
	}
	if _, dup := r.answered[location]; dup {
		return nil
	}
	now := b.now()
	r.answered[location] = now
	seen := map[string]bool{}
	for _, res := range results {
		for _, t := range r.Targets {
			k := t.Instance + "\x00" + t.Module
			if t.Instance != res.Target || t.Module != res.Module || seen[k] {
				continue
			}
			seen[k] = true
			res.Target, res.Module = t.Instance, t.Module
			r.Results = append(r.Results, Result{Location: location, TargetResult: res, Received: now})
		}
	}
	r.Missing = slices.DeleteFunc(r.Missing, func(l string) bool { return l == location })
	b.count("linkos_probe_now_answers_total", "Answers from probe locations to probe-on-demand requests", metrics.Labels("location", location))
	if len(r.Missing) == 0 && !r.Done {
		r.Done = true
		close(r.done)
	}
	return nil
}

// Await waits until every expected location has answered request id or ctx ends, then returns the
// request as it stands and marks it settled: it no longer counts against its token, though late answers
// are still taken until it expires. False when there is no such request.
func (b *Board) Await(ctx context.Context, id string) (View, bool) {
	b.mu.Lock()
	r := b.reqs[id]
	b.mu.Unlock()
	if r == nil {
		return View{}, false
	}
	select {
	case <-r.done:
	case <-ctx.Done():
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !r.settled {
		r.settled = true
		for _, l := range r.Missing {
			b.count("linkos_probe_now_missing_total", "Probe locations that did not answer a probe-on-demand request in time", metrics.Labels("location", l))
		}
	}
	return r.snapshot(), true
}
