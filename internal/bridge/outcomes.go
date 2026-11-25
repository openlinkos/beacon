package bridge

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

var (
	outcomeID    = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}-[0-9a-f]{12}-([0-9a-f]{16}|legacy)$`)
	outcomeGroup = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// IncidentOutcomesFor returns retained outcomes with related sessions within the machine selection.
func (b *Bridge) IncidentOutcomesFor(p store.Principal, query OutcomeQuery) (OutcomePage, error) {
	page, err := b.incidentOutcomes(query)
	if err == nil && (len(p.Machines) != 0 || p.MachineScopeEmpty()) {
		access := b.d.Store.SessionAccess(p)
		for i := range page.Outcomes {
			b.filterOutcomeSessions(access, &page.Outcomes[i])
		}
	}
	return page, err
}

// IncidentOutcomeFor returns one outcome and the related sessions within the machine selection.
func (b *Bridge) IncidentOutcomeFor(p store.Principal, id string) (OutcomeResult, error) {
	result, err := b.IncidentOutcome(id)
	if err == nil && (len(p.Machines) != 0 || p.MachineScopeEmpty()) {
		b.filterOutcomeSessions(b.d.Store.SessionAccess(p), &result.Outcome)
	}
	return result, err
}

func (b *Bridge) filterOutcomeSessions(access store.SessionAccess, outcome *store.IncidentOutcome) {
	outcome.Sessions = slices.DeleteFunc(outcome.Sessions, func(id string) bool {
		r, ok := b.d.Store.Session(id)
		return !ok || !access.Visible(r)
	})
}

func (b *Bridge) outcomeCutoff(now time.Time) time.Time {
	keep := b.d.Config.Storage.IncidentRetention.D()
	if keep == 0 {
		keep = config.DefaultIncidentRetention
	}
	return now.Add(-keep)
}

// actorKind classifies identified principals. Standalone credentials do not identify a human or agent.
func actorKind(who Actor) string {
	prefix, _, _ := strings.Cut(who.Key, ":")
	switch {
	case prefix == "agent":
		return "agent"
	case prefix == "user", who.Via == "feishu", who.Via == "pushover":
		return "person"
	}
	return "unattributed"
}

// recordParticipation keeps the latest recorded alert action per kind of actor.
func recordParticipation(g *store.Group, who Actor, action string, now time.Time) {
	kind := actorKind(who)
	p := store.Participation{Kind: kind, By: cleanLine(who.Name), Via: who.Via, Action: action, At: now}
	if p.NamedAcknowledgement() && (g.HumanAcknowledgement == nil || !p.At.Before(g.HumanAcknowledgement.At)) {
		g.HumanAcknowledgement = &store.OutcomeHumanAcknowledgement{By: p.By, Via: p.Via, At: p.At}
	}
	i := slices.IndexFunc(g.Participation, func(previous store.Participation) bool { return previous.Kind == kind })
	if i == -1 {
		g.Participation = append(g.Participation, p)
	} else if !p.At.Before(g.Participation[i].At) {
		g.Participation[i] = p
	}
}

func (b *Bridge) outcome(g store.Group, now, auditUntil time.Time) *store.IncidentOutcome {
	if g.Firing() || g.ResolvedAt.IsZero() || g.ResolvedAt.Before(b.outcomeCutoff(now)) {
		return nil
	}
	episode := g.Episode
	if episode == "" {
		episode = "legacy"
	}
	o := b.summariseEpisode(g)
	o.ID, o.AuditUntil = store.OutcomeID(g.ResolvedAt, g.ID, episode), auditUntil
	o.BindDeliveryReporters(g.Agents)
	return &o
}

func (b *Bridge) summariseEpisode(g store.Group) store.IncidentOutcome {
	services := make([]string, 0, maxServices)
	for _, service := range b.groupServices(&g) {
		services = append(services, cleanLine(service))
	}
	latest := newestProbeRun(g.ProbeRuns)
	human := g.HumanAcknowledgement
	if human == nil {
		// Older live groups may still have an explicit acknowledgement in participation. The
		// acknowledgement object itself can also be created by a silence, so keep it out of this test.
		for _, p := range g.Participation {
			if p.NamedAcknowledgement() {
				human = &store.OutcomeHumanAcknowledgement{By: p.By, Via: p.Via, At: p.At}
				break
			}
		}
	}
	// Older groups do not have typed participation. Report what their records actually identify.
	if len(g.Participation) == 0 {
		if g.Ack != nil {
			recordParticipation(&g, Actor{Name: g.Ack.By, Via: g.Ack.Via}, "ack", g.Ack.At)
		}
		if len(g.Notes) > 0 {
			n := g.Notes[len(g.Notes)-1]
			recordParticipation(&g, Actor{Name: n.By, Via: n.Via}, "note", n.At)
		}
		if latest != nil {
			recordParticipation(&g, Actor{Name: latest.By, Via: latest.Via}, "probe_now", latest.At)
		}
	}
	participants := append([]store.Participation{}, g.Participation...)
	slices.SortFunc(participants, func(a, c store.Participation) int { return c.At.Compare(a.At) })
	return store.IncidentOutcome{
		Group: g.ID, Title: cleanLine(b.title(&g)),
		Severity: g.Severity, OpeningSeverity: g.OpeningSeverity, Origin: g.Origin, ExerciseRun: g.ExerciseRun, Services: services, Started: g.StartsAt, Resolved: g.ResolvedAt,
		Participation: participants, Verification: verificationCounts(latest),
		PassingCheck: passingCheck(&g), AgentDeliveries: outcomeAgentDeliveries(g.Agents), HumanAcknowledgement: human,
	}
}

func outcomeAgentDeliveries(calls []store.AgentCall) []store.OutcomeAgentDelivery {
	if len(calls) == 0 {
		return nil
	}
	deliveries := make([]store.OutcomeAgentDelivery, 0, len(calls))
	for _, call := range calls {
		d := store.OutcomeAgentDelivery{
			ID: call.Delivery, Hook: cleanLine(call.Hook), Event: call.Event, Status: call.Status,
			Attempts: call.Attempts, QueuedAt: call.At, DoneAt: call.Done,
		}
		if call.Run != nil {
			report := *call.Run
			report.By = cleanLine(report.By)
			d.Run = &report
			if report.Status == "running" {
				d.StartedAt = report.At
			}
		}
		if call.RunStart != nil {
			d.StartedAt = call.RunStart.At
		}
		deliveries = append(deliveries, d)
	}
	return deliveries
}

func newestProbeRun(runs []store.ProbeRun) *store.ProbeRun {
	if len(runs) == 0 {
		return nil
	}
	latest := &runs[0]
	for i := 1; i < len(runs); i++ {
		if !runs[i].At.Before(latest.At) {
			latest = &runs[i]
		}
	}
	return latest
}

func (b *Bridge) saveGroup(g store.Group) error {
	return b.d.Store.PutGroupWithOutcome(g, b.outcome(g, b.now(), time.Time{}))
}

// archiveOutcome keeps a short list of session references when the live evidence goes away.
// Sessions and recordings remain subject to their own authorisation and retention settings.
func (b *Bridge) archiveOutcome(g store.Group, now, auditUntil time.Time) *store.IncidentOutcome {
	o := b.outcome(g, now, auditUntil)
	if o == nil {
		return nil
	}
	const keepRelatedSessions = 8
	sessions := b.sessions(&g, b.window(&g, defaultBefore), b.machines(&g), nil)
	for i := len(sessions) - 1; i >= max(0, len(sessions)-keepRelatedSessions); i-- {
		o.Sessions = append(o.Sessions, sessions[i].ID)
	}
	return o
}

// initOutcomes backfills recent resolved groups from pre-upgrade state.
func (b *Bridge) initOutcomes() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for _, g := range b.d.Store.Groups() {
		if summary := b.outcome(g, now, time.Time{}); summary != nil {
			if err := b.d.Store.EnsureIncidentOutcome(*summary); err != nil {
				b.d.Log.Error("backfill incident outcome", "group", g.ID, "err", err)
			}
		}
	}
	b.pruneOutcomes()
}

func (b *Bridge) pruneOutcomes() {
	if err := b.d.Store.PruneIncidentOutcomes(b.outcomeCutoff(b.now())); err != nil {
		b.d.Log.Error("prune incident outcomes", "err", err)
	}
}

// OutcomePage is the contract shared by the console, REST and MCP. Before is the next page's cursor.
type OutcomePage struct {
	Outcomes  []store.IncidentOutcome `json:"outcomes"`
	Before    string                  `json:"before,omitempty"`
	Untrusted Untrusted               `json:"untrusted"`
}

// IncidentOutcomes pages through retained alert episodes that reached rule recovery.
func (b *Bridge) IncidentOutcomes(group, before string, limit int) (OutcomePage, error) {
	return b.incidentOutcomes(OutcomeQuery{Group: group, Before: before, Limit: limit})
}

// OutcomeQuery filters retained incidents before applying the result limit and cursor.
type OutcomeQuery struct {
	Group, Before, Severity, Since, Origin, Evidence string
	Limit                                            int
}

func (b *Bridge) incidentOutcomes(q OutcomeQuery) (OutcomePage, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	switch {
	case q.Limit < 1 || q.Limit > 200:
		return OutcomePage{}, fmt.Errorf("%w: limit is 1 to 200", ErrBadInput)
	case q.Before != "" && !outcomeID.MatchString(q.Before):
		return OutcomePage{}, fmt.Errorf("%w: before must be an incident outcome ID", ErrBadInput)
	case q.Group != "" && !outcomeGroup.MatchString(q.Group):
		return OutcomePage{}, fmt.Errorf("%w: group must be an alert group ID", ErrBadInput)
	case q.Severity != "" && !slices.Contains(config.Severities, q.Severity):
		return OutcomePage{}, fmt.Errorf("%w: severity is critical, warning or info", ErrBadInput)
	case !store.ValidOutcomeOrigin(q.Origin):
		return OutcomePage{}, fmt.Errorf("%w: origin is exercise or unknown", ErrBadInput)
	case !store.ValidOutcomeEvidence(q.Evidence):
		return OutcomePage{}, fmt.Errorf("%w: evidence is agent_report, no_agent_report, human_ack, no_human_ack, passing_check, agent_passing_check or no_passing_check", ErrBadInput)
	}
	since := b.outcomeCutoff(b.now())
	if strings.TrimSpace(q.Since) != "" {
		from, err := b.since(q.Since, 0)
		if err != nil {
			return OutcomePage{}, err
		}
		if from.After(since) {
			since = from
		}
	}
	out, err := b.d.Store.IncidentOutcomes(store.OutcomeFilter{Since: since, Before: q.Before,
		Group: q.Group, Severity: q.Severity, Origin: q.Origin, Evidence: q.Evidence, Limit: q.Limit})
	if err != nil {
		return OutcomePage{}, err
	}
	p := OutcomePage{Outcomes: out, Untrusted: untrusted("outcomes[].title", "outcomes[].services",
		"outcomes[].agent_deliveries[].hook", "outcomes[].agent_deliveries[].run.by", "outcomes[].human_acknowledgement.by")}
	if len(out) > q.Limit {
		p.Outcomes, p.Before = out[:q.Limit], out[q.Limit-1].ID
	}
	return p, nil
}

var ErrOutcomeNotFound = errors.New("no such incident outcome")

// ArchivedIncidentID finds the incident's private summary when the live group holds a later episode.
func (b *Bridge) ArchivedIncidentID(group, episode string) (string, error) {
	if !outcomeGroup.MatchString(group) || !link.ValidEpisode(episode) {
		return "", ErrOutcomeNotFound
	}
	o, err := b.d.Store.IncidentOutcomeForEpisode(group, episode, b.outcomeCutoff(b.now()))
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrOutcomeNotFound
	}
	return o.ID, err
}

// IncidentOutcome reads the bounded, private summary of a resolved episode.
func (b *Bridge) IncidentOutcome(id string) (OutcomeResult, error) {
	if !outcomeID.MatchString(id) {
		return OutcomeResult{}, ErrOutcomeNotFound
	}
	o, err := b.d.Store.IncidentOutcome(id, b.outcomeCutoff(b.now()))
	if errors.Is(err, store.ErrNotFound) {
		return OutcomeResult{}, ErrOutcomeNotFound
	}
	return OutcomeResult{Outcome: o, Untrusted: untrusted("outcome.title", "outcome.services",
		"outcome.agent_deliveries[].hook", "outcome.agent_deliveries[].run.by", "outcome.human_acknowledgement.by")}, err
}

type OutcomeResult struct {
	Outcome   store.IncidentOutcome `json:"outcome"`
	Untrusted Untrusted             `json:"untrusted"`
}
