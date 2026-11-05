package secrets

import (
	"fmt"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/events"
)

// What a reviewer reads beside a request: the ask itself is the request's own row; around it, the
// design asks for the alert it hangs on (with the way to pull the evidence), the asker's recent
// record, and the signs that this ask is out of the ordinary — the same class of thing the on-call
// agent's evidence is. The review agent reaches it through get_request, and the bot's judgement
// stays the bot's: Beacon puts the facts beside the ask and nothing more.

// RequestWithContext is get_request's answer to a reviewer: the request and what surrounds it.
type RequestWithContext struct {
	Request Request       `json:"request"`
	Context *ReviewContext `json:"context,omitempty"`
}

// ReviewContext is what surrounds an ask: its place in the chain, the alert it hangs on, the
// asker's recent record, and the anomaly signals the ask raises.
type ReviewContext struct {
	Chain   ChainState   `json:"chain"`
	Alert   *AlertDigest `json:"alert,omitempty"`
	History AgentHistory `json:"history"`
	Signals []string     `json:"signals,omitempty"`
}

// ChainState is where an ask stands in its review chain.
type ChainState struct {
	Layers    []string   `json:"layers,omitempty"` // the chain the ask walks
	At        int        `json:"at"`               // the layer it stands at (-1: nowhere; the row predates the chain)
	Approvals int        `json:"approvals"`
	Needed    int        `json:"needed"`
	Deadline  time.Time  `json:"deadline,omitzero"`
}

// AlertDigest is the alert an ask hangs on: whether it is firing, the labels it carries (its
// service among them), and the one call that brings the evidence.
type AlertDigest struct {
	ID       string            `json:"id"`
	Firing   bool              `json:"firing"`
	Labels   map[string]string `json:"labels,omitempty"`
	Evidence string            `json:"evidence,omitempty"`
}

// AgentHistory is the asker's recent record: the grants and denials of the last week and the uses
// of the last day against the week before.
type AgentHistory struct {
	Grants7d     int  `json:"grants_7d"`
	Denied7d     int  `json:"denied_7d"`
	Uses24h      int  `json:"uses_24h"`
	Uses7dBefore int  `json:"uses_7d_before"`
	FirstAsk     bool `json:"first_ask"` // no grant and no earlier ask of this collection
}

// ReviewContextOf builds the context of one request. It reads no value and no detail: the counts,
// the labels and the chain — the facts a reviewer weighs, never the secrets themselves.
func (s *Service) ReviewContextOf(r Request) ReviewContext {
	ctx := ReviewContext{Chain: ChainState{Layers: r.Chain, At: -1, Approvals: r.Approvals, Needed: r.Needed,
		Deadline: r.Deadline}}
	if len(r.Chain) > 0 {
		ctx.Chain.At = r.Step
	}
	now := s.now()

	// the alert the ask hangs on
	if r.Alert != "" && s.d.AlertInfo != nil {
		if firing, labels, ok := s.d.AlertInfo(r.Alert); ok {
			ctx.Alert = &AlertDigest{ID: r.Alert, Firing: firing, Labels: labels,
				Evidence: fmt.Sprintf("GET /api/v1/alerts/%s/evidence (status:read), or the MCP tool get_evidence with alert.id %s", r.Alert, r.Alert)}
		}
	}

	// the asker's record
	week := now.Add(-7 * 24 * time.Hour)
	for _, g := range s.GrantsOf(r.Agent) {
		if g.Created.After(week) {
			ctx.History.Grants7d++
		}
	}
	collection := "" // the first item's collection, by ID
	if len(r.Items) > 0 && r.Items[0].ID != "" {
		if item, _, ok := s.Item(r.Items[0].ID); ok {
			collection = item.Collection
		}
	}
	if collection != "" {
		ctx.History.FirstAsk = true
		for _, g := range s.GrantsOf(r.Agent) {
			if g.Collection == collection {
				ctx.History.FirstAsk = false
			}
		}
	}
	for _, past := range s.Requests("") {
		if past.ID == r.ID || past.Agent != r.Agent {
			continue
		}
		if past.Status == ReqDenied && past.Created.After(week) {
			ctx.History.Denied7d++
		}
		if ctx.History.FirstAsk {
			for _, it := range past.Items {
				if item, _, ok := s.Item(it.ID); ok && item.Collection == collection {
					ctx.History.FirstAsk = false
				}
			}
		}
	}
	if log := s.d.Store.UsesLog(); log != nil {
		if evs, _, err := log.Query(events.Filter{Actor: r.Agent, From: now.Add(-24 * time.Hour), To: now, Limit: 10000}); err == nil {
			ctx.History.Uses24h = len(evs)
		}
		if evs, _, err := log.Query(events.Filter{Actor: r.Agent, From: now.Add(-8 * 24 * time.Hour), To: now.Add(-24 * time.Hour), Limit: 10000}); err == nil {
			ctx.History.Uses7dBefore = len(evs)
		}
	}

	// the signals
	if ctx.History.FirstAsk && collection != "" {
		ctx.Signals = append(ctx.Signals, "the asker has neither a grant nor an earlier ask of this collection")
	}
	if ctx.History.Uses24h > 10 && float64(ctx.History.Uses24h) > 5*(float64(ctx.History.Uses7dBefore)/7+1) {
		ctx.Signals = append(ctx.Signals, fmt.Sprintf("usage is up: %d uses in the last day against a 7-day mean of %d",
			ctx.History.Uses24h, ctx.History.Uses7dBefore/7))
	}
	if hour := r.Created.UTC().Hour(); s.hasUseHours(r.Agent) && !s.useHourHas(r.Agent, hour) {
		ctx.Signals = append(ctx.Signals, fmt.Sprintf("the ask sits outside the asker's usual hours (last use at %02d:00 UTC not among them)", hour))
	}
	return ctx
}

// useHours caches the hours an agent has used secrets in, for the usual-hours signal. The sweep of
// a review page is not the place for a query per ask.
func (s *Service) hasUseHours(agent string) bool {
	s.hoursMu.Lock()
	defer s.hoursMu.Unlock()
	s.loadUseHours(agent)
	return len(s.useHours[agent]) > 0
}

func (s *Service) useHourHas(agent string, hour int) bool {
	s.hoursMu.Lock()
	defer s.hoursMu.Unlock()
	s.loadUseHours(agent)
	return s.useHours[agent][hour]
}

// loadUseHours reads the agent's hours of the last week; the caller holds hoursMu.
func (s *Service) loadUseHours(agent string) {
	if s.useHoursAt[agent].After(s.now().Add(-24 * time.Hour)) {
		return // fresh enough
	}
	hours := map[int]bool{}
	if log := s.d.Store.UsesLog(); log != nil {
		if evs, _, err := log.Query(events.Filter{Actor: agent, From: s.now().Add(-7 * 24 * time.Hour), Limit: 10000}); err == nil {
			for _, e := range evs {
				hours[e.Time.UTC().Hour()] = true
			}
		}
	}
	if s.useHours == nil {
		s.useHours = map[string]map[int]bool{}
	}
	if s.useHoursAt == nil {
		s.useHoursAt = map[string]time.Time{}
	}
	s.useHours[agent], s.useHoursAt[agent] = hours, s.now()
}

// UseHoursForget drops the cached hours (the tests' clock moves).
func (s *Service) UseHoursForget() {
	s.hoursMu.Lock()
	s.useHours, s.useHoursAt = nil, nil
	s.hoursMu.Unlock()
}

// wordsOf writes the chain for people: the layers with the one now deciding marked.
func (c ChainState) Words() string {
	if len(c.Layers) == 0 {
		return ""
	}
	var parts []string
	for i, l := range c.Layers {
		if i == c.At {
			parts = append(parts, "["+l+"]")
		} else {
			parts = append(parts, l)
		}
	}
	out := strings.Join(parts, " → ")
	if c.Needed > 1 {
		out += fmt.Sprintf(" (%d of %d approvals)", c.Approvals, c.Needed)
	}
	return out
}
