package bridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/store"
)

// Probing on demand (probe_now): after an agent has fixed something, it has every location that probes
// the target probe it again at once and sees each location's answer, instead of waiting for the next
// cycle and the rule engine. The board (package probenow) holds the requests and the gateway wakes the
// probes; this is the operation the API and MCP call, audited and echoed like the other writes.

// Errors of probing on demand. ErrProbeBusy and ErrProbeNotFound are the board's, with the detail in the
// message; ErrProbeUnavailable says there is no board (no gateway, so no probes).
var (
	ErrProbeBusy        = probenow.ErrBusy
	ErrProbeNotFound    = probenow.ErrNotFound
	ErrProbeUnavailable = errors.New("probing on demand is not available: this Beacon has no gateway for probes")
)

const (
	probeWaitDefault = 45 * time.Second
	probeWaitMax     = 90 * time.Second
	keepProbeRuns    = 10
)

// ProbeNowInput is what to probe: one of AlertID (the targets of the group's alerts), Target (with an
// optional Module) or Service; and how many seconds to wait for the locations (0: the default).
type ProbeNowInput struct {
	AlertID string
	Target  string
	Module  string
	Service string
	Wait    int
}

// ProbeTarget is one target of a request and the locations expected to probe it.
type ProbeTarget struct {
	Instance  string   `json:"instance"`
	Module    string   `json:"module"`
	Service   string   `json:"service,omitempty"`
	Locations []string `json:"locations"`
}

// ProbeNowResult is one location's answer for one target: the fields the evidence shows, fresh.
// ProbedAt is when the answer reached Beacon.
type ProbeNowResult struct {
	Location        string     `json:"location"`
	Instance        string     `json:"instance"`
	Module          string     `json:"module"`
	Service         string     `json:"service,omitempty"`
	OK              bool       `json:"ok"`
	Reason          string     `json:"reason,omitempty"`
	DurationSeconds float64    `json:"duration_seconds"`
	HTTPStatus      int        `json:"http_status,omitempty"`
	Attempts        int        `json:"attempts,omitempty"`
	CertExpires     *time.Time `json:"cert_expires,omitempty"`
	ProbedAt        time.Time  `json:"probed_at"`
}

// ProbeNow is the answer: what was probed, each location's result (failing first), the locations that
// did not answer in time, and a one-line summary.
type ProbeNow struct {
	ID        string           `json:"id"`
	Targets   []ProbeTarget    `json:"targets"`
	Locations []string         `json:"locations"` // expected to answer
	Results   []ProbeNowResult `json:"results"`
	Missing   []string         `json:"missing"`  // locations with at least one missing result
	Complete  bool             `json:"complete"` // every expected result arrived
	Summary   string           `json:"summary"`
	Untrusted Untrusted        `json:"untrusted"`
}

// probeWants reads what the input asks for, and the alert episode the result belongs to, if any.
func (b *Bridge) probeWants(in ProbeNowInput) ([]probenow.Want, time.Time, error) {
	given := 0
	for _, s := range []string{in.AlertID, in.Target, in.Service} {
		if strings.TrimSpace(s) != "" {
			given++
		}
	}
	switch {
	case given != 1:
		return nil, time.Time{}, fmt.Errorf("%w: give one of alert_id, target or service", ErrBadInput)
	case in.Module != "" && in.Target == "":
		return nil, time.Time{}, fmt.Errorf("%w: module goes with target", ErrBadInput)
	case in.Target != "":
		return []probenow.Want{{Target: strings.TrimSpace(in.Target), Module: strings.TrimSpace(in.Module)}}, time.Time{}, nil
	case in.Service != "":
		return []probenow.Want{{Service: strings.TrimSpace(in.Service)}}, time.Time{}, nil
	}
	g, ok := b.d.Store.Group(in.AlertID)
	if !ok {
		return nil, time.Time{}, ErrNotFound
	}
	// the alerts' own targets first (instance and module, as probe rules carry them), else their services
	var wants []probenow.Want
	for _, a := range g.Alerts {
		if inst := a.Labels["instance"]; inst != "" {
			w := probenow.Want{Target: inst, Module: a.Labels["module"]}
			if !slices.Contains(wants, w) {
				wants = append(wants, w)
			}
		}
	}
	if len(wants) == 0 {
		for _, s := range b.groupServices(&g) {
			wants = append(wants, probenow.Want{Service: s})
		}
	}
	if len(wants) == 0 {
		return nil, time.Time{}, fmt.Errorf("%w: the alert has neither instance nor service labels, so no probes belong to it", ErrBadInput)
	}
	return wants, g.StartsAt, nil
}

// CanProbeNow reports whether this Beacon has a gateway for on-demand checks.
func (b *Bridge) CanProbeNow() bool { return b.d.ProbeNow != nil }

// ProbeNow has every location that probes the input's targets probe them again at once, waits for their
// answers (in.Wait seconds, 45 by default and at most 90) and returns them per location.
func (b *Bridge) ProbeNow(ctx context.Context, who Actor, in ProbeNowInput) (ProbeNow, error) {
	if !b.CanProbeNow() {
		return ProbeNow{}, ErrProbeUnavailable
	}
	wait := time.Duration(in.Wait) * time.Second
	switch {
	case in.Wait == 0:
		wait = probeWaitDefault
	case wait < time.Second || wait > probeWaitMax:
		return ProbeNow{}, fmt.Errorf("%w: wait is 1 to %d seconds", ErrBadInput, int(probeWaitMax.Seconds()))
	}
	wants, starts, err := b.probeWants(in)
	if err != nil {
		return ProbeNow{}, err
	}
	v, err := b.d.ProbeNow.Create(who.Name, wants)
	if err != nil {
		if errors.Is(err, probenow.ErrTooMany) {
			return ProbeNow{}, fmt.Errorf("%w: %s", ErrBadInput, strings.TrimPrefix(err.Error(), probenow.ErrTooMany.Error()+": "))
		}
		return ProbeNow{}, err
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if settled, ok := b.d.ProbeNow.Await(wctx, v.ID); ok {
		v = settled
	}
	observations := probeObservations(v)
	tally := tallyProbes(observations)
	out := probeNowView(v, tally)
	what := b.probeWhat(b.lang, v)
	audit := store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "probe_now", Target: cleanLine(b.probeWhat(i18n.EN, v)), Detail: out.Summary}
	if in.AlertID != "" {
		audit.Target = in.AlertID
		audit.Detail = fmt.Sprintf("%s: %s", v.ID, out.Summary)
		b.recordProbeRun(in.AlertID, starts, store.ProbeRun{ID: v.ID, At: b.now(), By: who.Name, Via: who.Via, Results: observations})
	}
	_ = b.d.Store.Audit(audit)
	b.echo(ctx, b.t("%s had %d locations probe %s again via %s: %s", who.Name, len(v.Expected), chatSafe(cleanLine(what)), b.viaName(who.Via), tally.summary(b.lang)))
	return out, nil
}

// probeObservations records every expected target-location pair, even when a probe replied but
// omitted a target.
func probeObservations(v probenow.View) []store.ProbeObservation {
	type point struct{ location, target, module string }
	answers := make(map[point]probenow.Result, len(v.Results))
	for _, r := range v.Results {
		answers[point{r.Location, r.Target, r.Module}] = r
	}
	var observations []store.ProbeObservation
	for _, target := range v.Targets {
		for _, location := range target.Locations {
			item := store.ProbeObservation{Location: location, Instance: cleanLine(target.Instance), Module: cleanLine(target.Module), Status: "missing"}
			if r, ok := answers[point{location, target.Instance, target.Module}]; ok {
				item.Status, item.HTTPCode = "ok", r.HTTPStatus
				if !r.Success {
					item.Status, item.Reason = "failed", clean(r.Reason, maxShort, false)
				}
			}
			observations = append(observations, item)
		}
	}
	return observations
}

// recordProbeRun saves the results on the alert episode the caller actually checked.
func (b *Bridge) recordProbeRun(id string, starts time.Time, run store.ProbeRun) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok || !g.StartsAt.Equal(starts) {
		return
	}
	g.ProbeRuns = append(g.ProbeRuns, run)
	if len(g.ProbeRuns) > keepProbeRuns {
		g.ProbeRuns = g.ProbeRuns[len(g.ProbeRuns)-keepProbeRuns:]
	}
	if err := b.d.Store.PutGroup(g); err != nil {
		b.d.Log.Error("recording on-demand probe results", "group", id, "request", run.ID, "err", err)
	}
}

// probeWhat names what was probed in one phrase: the target, or how many targets of which services.
func (b *Bridge) probeWhat(l i18n.Lang, v probenow.View) string {
	if len(v.Targets) == 1 {
		return v.Targets[0].Instance
	}
	var svcs []string
	for _, t := range v.Targets {
		if t.Service != "" && !slices.Contains(svcs, t.Service) {
			svcs = append(svcs, t.Service)
		}
	}
	slices.Sort(svcs)
	return i18n.T(l, "%d targets of %s", len(v.Targets), strings.Join(svcs, ", "))
}

type probeTally struct {
	passed, failed   int
	missing, reasons []string
}

// summary counts locations. A location is OK only when all of its expected results passed.
func (t probeTally) summary(l i18n.Lang) string {
	var parts []string
	if t.passed > 0 || (t.failed == 0 && len(t.missing) == 0) {
		parts = append(parts, i18n.T(l, "%d OK", t.passed))
	}
	if t.failed > 0 {
		parts = append(parts, i18n.T(l, "%d failing (%s)", t.failed, strings.Join(t.reasons, ", ")))
	}
	if len(t.missing) > 0 {
		parts = append(parts, i18n.T(l, "%d did not answer", len(t.missing)))
	}
	return strings.Join(parts, ", ")
}

func tallyProbes(observations []store.ProbeObservation) probeTally {
	type result struct{ failed, missing bool }
	var tally probeTally
	locations := map[string]result{}
	for _, o := range observations {
		r := locations[o.Location]
		switch o.Status {
		case "failed":
			r.failed = true
			if !slices.Contains(tally.reasons, o.Reason) {
				tally.reasons = append(tally.reasons, o.Reason)
			}
		case "missing":
			r.missing = true
		}
		locations[o.Location] = r
	}
	for location, r := range locations {
		if r.failed {
			tally.failed++
		}
		if r.missing {
			tally.missing = append(tally.missing, location)
		}
		if !r.failed && !r.missing {
			tally.passed++
		}
	}
	slices.Sort(tally.missing)
	slices.Sort(tally.reasons)
	return tally
}

// probeNowView is the answer for an agent, its text from the target sets and the probes cleaned.
func probeNowView(v probenow.View, tally probeTally) ProbeNow {
	svcs := map[string]bool{}
	for _, t := range v.Targets {
		svcs[t.Service] = true
	}
	out := ProbeNow{ID: v.ID, Targets: make([]ProbeTarget, 0, len(v.Targets)), Locations: v.Expected, Results: make([]ProbeNowResult, 0, len(v.Results)),
		Missing: tally.missing, Complete: len(tally.missing) == 0, Summary: tally.summary(i18n.EN),
		Untrusted: untrusted("targets[].instance", "targets[].module", "targets[].service", "results[].instance", "results[].module",
			"results[].service", "results[].reason", "summary")}
	if out.Locations == nil {
		out.Locations = []string{}
	}
	if out.Missing == nil {
		out.Missing = []string{}
	}
	for _, t := range v.Targets {
		out.Targets = append(out.Targets, ProbeTarget{Instance: cleanLine(t.Instance), Module: cleanLine(t.Module), Service: cleanLine(t.Service), Locations: t.Locations})
	}
	for _, r := range v.Results {
		res := ProbeNowResult{Location: r.Location, Instance: cleanLine(r.Target), Module: cleanLine(r.Module), OK: r.Success,
			DurationSeconds: math.Round(r.DurationSeconds*1000) / 1000, HTTPStatus: r.HTTPStatus, Attempts: r.Attempts, ProbedAt: r.Received}
		if !r.Success {
			res.Reason = clean(r.Reason, maxShort, false)
		}
		if r.CertExpiry > 0 {
			t := time.Unix(r.CertExpiry, 0).UTC()
			res.CertExpires = &t
		}
		if len(svcs) > 1 {
			for _, t := range v.Targets {
				if t.Instance == r.Target && t.Module == r.Module {
					res.Service = cleanLine(t.Service)
					break
				}
			}
		}
		out.Results = append(out.Results, res)
	}
	sort.Slice(out.Results, func(i, j int) bool {
		a, c := out.Results[i], out.Results[j]
		if a.OK != c.OK {
			return !a.OK
		}
		return a.Location+"\x00"+a.Instance+"\x00"+a.Module < c.Location+"\x00"+c.Instance+"\x00"+c.Module
	})
	return out
}
