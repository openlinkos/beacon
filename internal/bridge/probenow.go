package bridge

import (
	"cmp"
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
	"github.com/openlinkos/beacon/internal/web/link"
)

// Probing on demand (probe_now): after an agent has fixed something, it has every location that probes
// the target probe it again at once and sees each location's answer, instead of waiting for the next
// cycle and the rule engine. The board (package probenow) holds the requests and the gateway wakes the
// probes; this is the operation the API and MCP call, audited and echoed like the other writes.

// Errors of probing on demand. The board reports busy and not found. Checks are unavailable without
// a board, and an incident check requires a confirmed write of its result and audit entry.
var (
	ErrProbeBusy        = probenow.ErrBusy
	ErrProbeNotFound    = probenow.ErrNotFound
	ErrProbeUnavailable = errors.New("probing on demand is not available: this Beacon has no gateway for probes")
	ErrProbeNotRecorded = errors.New("could not save the check for this incident")
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
	Episode *string // when set, only verify this alert episode
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
	Episode   string           `json:"episode,omitempty"` // the incident selected for the check
	Targets   []ProbeTarget    `json:"targets"`
	Locations []string         `json:"locations"` // expected to answer
	Results   []ProbeNowResult `json:"results"`
	Missing   []string         `json:"missing"`  // locations with at least one missing result
	Complete  bool             `json:"complete"` // every expected result arrived
	Summary   string           `json:"summary"`
	Untrusted Untrusted        `json:"untrusted"`
}

// probeWants reads what the input asks for, and the alert episode the result belongs to, if any.
func (b *Bridge) probeWants(in ProbeNowInput) ([]probenow.Want, probeEpisode, error) {
	given := 0
	for _, s := range []string{in.AlertID, in.Target, in.Service} {
		if strings.TrimSpace(s) != "" {
			given++
		}
	}
	switch {
	case given != 1:
		return nil, probeEpisode{}, fmt.Errorf("%w: give one of alert_id, target or service", ErrBadInput)
	case in.Episode != nil && strings.TrimSpace(in.AlertID) == "":
		return nil, probeEpisode{}, fmt.Errorf("%w: expected_episode goes with alert_id", ErrBadInput)
	case in.Episode != nil && *in.Episode != "" && !link.ValidEpisode(*in.Episode):
		return nil, probeEpisode{}, fmt.Errorf("%w: expected_episode must be an episode identifier", ErrBadInput)
	case in.Module != "" && in.Target == "":
		return nil, probeEpisode{}, fmt.Errorf("%w: module goes with target", ErrBadInput)
	case in.Target != "":
		return []probenow.Want{{Target: strings.TrimSpace(in.Target), Module: strings.TrimSpace(in.Module)}}, probeEpisode{}, nil
	case in.Service != "":
		return []probenow.Want{{Service: strings.TrimSpace(in.Service)}}, probeEpisode{}, nil
	}
	g, ok := b.d.Store.Group(in.AlertID)
	if !ok {
		return nil, probeEpisode{}, ErrNotFound
	}
	if in.Episode != nil && *in.Episode != g.Episode {
		return nil, probeEpisode{}, ErrEpisodeChanged
	}
	wants, err := incidentProbeWants(g)
	if err != nil {
		return nil, probeEpisode{}, err
	}
	return wants, probeEpisode{starts: g.StartsAt, key: g.Episode}, nil
}

// incidentProbeWants preserves each alert member's selection, including aggregated rules which
// name a service and domain without an instance. Matching one member cannot substitute for another.
func incidentProbeWants(g store.Group) ([]probenow.Want, error) {
	members := g.Alerts
	if len(members) == 0 {
		members = []store.Alert{{}} // older group records may only have common labels
	}
	wants := make([]probenow.Want, 0, len(members))
	seen := make(map[probenow.Want]bool)
	for _, member := range members {
		label := func(name string) string { return cmp.Or(member.Labels[name], g.Common[name], g.Labels[name]) }
		want := probenow.Want{Target: label("instance"), Module: label("module"), Service: label("service"), FailureDomain: label("failure_domain")}
		if want.Target == "" && want.Service == "" {
			return nil, fmt.Errorf("%w: an alert member has neither instance nor service labels, so no probes belong to it", ErrBadInput)
		}
		if !seen[want] {
			seen[want] = true
			wants = append(wants, want)
		}
	}
	return wants, nil
}

type probeEpisode struct {
	starts time.Time
	key    string
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
	wants, episode, err := b.probeWants(in)
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
	out.Episode = episode.key
	what := b.probeWhat(b.lang, v)
	audit := store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "probe_now", Target: cleanLine(b.probeWhat(i18n.EN, v)), Detail: out.Summary}
	if in.AlertID == "" {
		_ = b.d.Store.Audit(audit)
	} else {
		audit.Target = in.AlertID
		audit.Time = b.now()
		run := store.ProbeRun{ID: v.ID, At: audit.Time, By: who.Name, Via: who.Via, Results: observations}
		err := b.recordProbeRun(in.AlertID, episode, who, run, out.Summary)
		switch {
		case err == nil:
			// The check and its audit entry were committed together.
		case !errors.Is(err, ErrEpisodeChanged):
			return ProbeNow{}, err
		case in.Episode != nil:
			audit.Detail = fmt.Sprintf("%s: episode %q changed before verification could be recorded", v.ID, episode.key)
			_ = b.d.Store.Audit(audit)
			return ProbeNow{}, err
		default:
			audit.Detail = fmt.Sprintf("%s: %s", v.ID, out.Summary)
			_ = b.d.Store.Audit(audit)
		}
	}
	b.echo(ctx, b.t("%s had %d locations probe %s again via %s: %s", who.Name, len(v.Expected), chatSafe(cleanLine(what)), b.viaName(who.Via), tally.summary(b.lang)))
	return out, nil
}

type probePoint struct{ location, target, module string }

// probeObservations records every expected target-location pair, even when a probe replied but
// omitted a target.
func probeObservations(v probenow.View) []store.ProbeObservation {
	answers := make(map[probePoint]probenow.Result, len(v.Results))
	for _, r := range v.Results {
		answers[probePoint{r.Location, r.Target, r.Module}] = r
	}
	// The wire result identifies a target and module, so a result that could belong to several
	// requested checks (for example different label sets) cannot verify any one of them.
	expected := make(map[probePoint]int)
	for _, target := range v.Targets {
		for _, location := range target.Locations {
			expected[probePoint{location, target.Instance, target.Module}]++
		}
	}
	var observations []store.ProbeObservation
	for _, target := range v.Targets {
		for _, location := range target.Locations {
			item := store.ProbeObservation{Location: location, Instance: cleanLine(target.Instance), Module: cleanLine(target.Module), Status: "missing"}
			key := probePoint{location, target.Instance, target.Module}
			if r, ok := answers[key]; ok && expected[key] == 1 {
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

// recordProbeRun saves the check, the resolved outcome if present, and its audit entry together.
func (b *Bridge) recordProbeRun(id string, episode probeEpisode, who Actor, run store.ProbeRun, summary string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok || !g.StartsAt.Equal(episode.starts) || g.Episode != episode.key {
		return ErrEpisodeChanged
	}
	run.ActorKind = actorKind(who)
	g.PassingCheck = preferPassingCheck(passingCheck(&g), verifyPassingCheck(&run), g.ResolvedAt)
	g.ProbeRuns = append(g.ProbeRuns, run)
	if len(g.ProbeRuns) > keepProbeRuns {
		g.ProbeRuns = g.ProbeRuns[len(g.ProbeRuns)-keepProbeRuns:]
	}
	recordParticipation(&g, who, "probe_now", run.At)
	audit := store.AuditEntry{Time: run.At, Actor: run.By, Via: run.Via, Action: "probe_now", Target: id,
		Detail: fmt.Sprintf("%s: %s", run.ID, summary)}
	if err := b.d.Store.PutGroupAndAudit(g, b.outcome(g, b.now(), time.Time{}), audit); err != nil {
		b.d.Log.Error("recording on-demand probe results", "group", id, "request", run.ID, "err", err)
		return ErrProbeNotRecorded
	}
	return nil
}

func verificationCounts(run *store.ProbeRun) *store.Verification {
	if run == nil {
		return nil
	}
	v := &store.Verification{ID: run.ID, At: run.At, By: cleanLine(run.By), Via: run.Via, ActorKind: run.ActorKind}
	for _, p := range run.Results {
		switch p.Status {
		case "ok":
			v.Passed++
		case "failed":
			v.Failed++
		default:
			v.Missing++
		}
	}
	return v
}

func verifyPassingCheck(run *store.ProbeRun) *store.Verification {
	if run.ID == "" || run.At.IsZero() || len(run.Results) == 0 {
		return nil
	}
	seen := make(map[probePoint]bool, len(run.Results))
	for _, p := range run.Results {
		key := probePoint{p.Location, p.Instance, p.Module}
		if p.Status != "ok" || p.Location == "" || p.Instance == "" || seen[key] {
			return nil
		}
		seen[key] = true
	}
	return verificationCounts(run)
}

// passingCheck also uses still-retained runs from older groups that had no separate summary.
func passingCheck(g *store.Group) *store.Verification {
	if g.PassingCheck != nil {
		return g.PassingCheck
	}
	var best *store.Verification
	for i := range g.ProbeRuns {
		best = preferPassingCheck(best, verifyPassingCheck(&g.ProbeRuns[i]), g.ResolvedAt)
	}
	return best
}

// preferPassingCheck takes the last complete pass by rule recovery or, if there is none, the first
// complete pass afterwards. A firing group has no recovery boundary yet. Keep the first at a tie.
func preferPassingCheck(current, candidate *store.Verification, resolved time.Time) *store.Verification {
	if current == nil {
		return candidate
	}
	if candidate == nil {
		return current
	}
	currentAfter := !resolved.IsZero() && current.At.After(resolved)
	candidateAfter := !resolved.IsZero() && candidate.At.After(resolved)
	switch {
	case currentAfter && (!candidateAfter || candidate.At.Before(current.At)):
		return candidate
	case !currentAfter && !candidateAfter && candidate.At.After(current.At):
		return candidate
	default:
		return current
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
