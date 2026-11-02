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
	Missing   []string         `json:"missing"`
	Complete  bool             `json:"complete"` // every expected location answered
	Summary   string           `json:"summary"`
	Untrusted Untrusted        `json:"untrusted"`
}

// probeWants reads what the input asks for, as the board takes it, and the words for the chat.
func (b *Bridge) probeWants(in ProbeNowInput) ([]probenow.Want, error) {
	given := 0
	for _, s := range []string{in.AlertID, in.Target, in.Service} {
		if strings.TrimSpace(s) != "" {
			given++
		}
	}
	switch {
	case given != 1:
		return nil, fmt.Errorf("%w: give one of alert_id, target or service", ErrBadInput)
	case in.Module != "" && in.Target == "":
		return nil, fmt.Errorf("%w: module goes with target", ErrBadInput)
	case in.Target != "":
		return []probenow.Want{{Target: strings.TrimSpace(in.Target), Module: strings.TrimSpace(in.Module)}}, nil
	case in.Service != "":
		return []probenow.Want{{Service: strings.TrimSpace(in.Service)}}, nil
	}
	g, ok := b.d.Store.Group(in.AlertID)
	if !ok {
		return nil, ErrNotFound
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
		return nil, fmt.Errorf("%w: the alert has neither instance nor service labels, so no probes belong to it", ErrBadInput)
	}
	return wants, nil
}

// ProbeNow has every location that probes the input's targets probe them again at once, waits for their
// answers (in.Wait seconds, 45 by default and at most 90) and returns them per location.
func (b *Bridge) ProbeNow(ctx context.Context, who Actor, in ProbeNowInput) (ProbeNow, error) {
	if b.d.ProbeNow == nil {
		return ProbeNow{}, ErrProbeUnavailable
	}
	wait := time.Duration(in.Wait) * time.Second
	switch {
	case in.Wait == 0:
		wait = probeWaitDefault
	case wait < time.Second || wait > probeWaitMax:
		return ProbeNow{}, fmt.Errorf("%w: wait is 1 to %d seconds", ErrBadInput, int(probeWaitMax.Seconds()))
	}
	wants, err := b.probeWants(in)
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
	v, _ = b.d.ProbeNow.Await(wctx, v.ID)
	out := b.probeNowView(v)
	what := b.probeWhat(b.lang, v)
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "probe_now", Target: cleanLine(b.probeWhat(i18n.EN, v)), Detail: out.Summary})
	b.echo(ctx, b.t("%s had %d locations probe %s again via %s: %s", who.Name, len(v.Expected), chatSafe(cleanLine(what)), b.viaName(who.Via), b.probeSummary(b.lang, v)))
	return out, nil
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

// probeSummary is the one line: how many locations are OK, how many fail and why, how many did not
// answer. A location with several targets is OK when all of them are.
func (b *Bridge) probeSummary(l i18n.Lang, v probenow.View) string {
	okBy := map[string]bool{}
	reasons := map[string]bool{}
	for _, r := range v.Results {
		if _, seen := okBy[r.Location]; !seen {
			okBy[r.Location] = true
		}
		if !r.Success {
			okBy[r.Location] = false
			reasons[clean(r.Reason, maxShort, false)] = true
		}
	}
	ok, failing := 0, 0
	for _, good := range okBy {
		if good {
			ok++
		} else {
			failing++
		}
	}
	var parts []string
	if ok > 0 || (failing == 0 && len(v.Missing) == 0) {
		parts = append(parts, i18n.T(l, "%d OK", ok))
	}
	if failing > 0 {
		parts = append(parts, i18n.T(l, "%d failing (%s)", failing, strings.Join(slices.Sorted(func(yield func(string) bool) {
			for r := range reasons {
				if !yield(r) {
					return
				}
			}
		}), ", ")))
	}
	if len(v.Missing) > 0 {
		parts = append(parts, i18n.T(l, "%d did not answer", len(v.Missing)))
	}
	return strings.Join(parts, ", ")
}

// probeNowView is the answer for an agent, its text from the target sets and the probes cleaned.
func (b *Bridge) probeNowView(v probenow.View) ProbeNow {
	svcs := map[string]bool{}
	for _, t := range v.Targets {
		svcs[t.Service] = true
	}
	out := ProbeNow{ID: v.ID, Targets: make([]ProbeTarget, 0, len(v.Targets)), Locations: v.Expected, Results: make([]ProbeNowResult, 0, len(v.Results)),
		Missing: v.Missing, Complete: v.Done, Summary: b.probeSummary(i18n.EN, v),
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
