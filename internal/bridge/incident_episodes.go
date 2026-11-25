package bridge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

const episodeReadBudget = 3 * time.Second
const maxEpisodeWindow = 31 * 24 * time.Hour

// IncidentEpisodeQuery selects a fixed window of reported alert starts, whether still open or recovered.
type IncidentEpisodeQuery struct {
	Since, Until, Before, Group, Severity, Origin string
	Limit                                         int
}

// IncidentEpisode is a private, compact accounting row. Its evidence records distinct observations.
type IncidentEpisode struct {
	Group                string                             `json:"group"`
	Episode              string                             `json:"episode"`
	OutcomeID            string                             `json:"outcome_id,omitempty"`
	State                string                             `json:"state"`
	Title                string                             `json:"title"`
	Services             []string                           `json:"services"`
	Severity             string                             `json:"severity"`
	OpeningSeverity      string                             `json:"opening_severity"`
	Origin               string                             `json:"origin"`
	ExerciseRun          string                             `json:"exercise_run,omitempty"`
	Started              time.Time                          `json:"started_at"`
	Resolved             *time.Time                         `json:"resolved_at,omitempty"`
	Wake                 string                             `json:"wake"`
	AgentDeliveries      []store.OutcomeAgentDelivery       `json:"agent_deliveries,omitempty"`
	Participation        []store.Participation              `json:"participation"`
	HumanAcknowledgement *store.OutcomeHumanAcknowledgement `json:"human_acknowledgement,omitempty"`
	Verification         *store.Verification                `json:"verification,omitempty"`
	PassingCheck         *store.Verification                `json:"passing_check,omitempty"`
}

// IncidentEpisodePage holds open and recovered rows and the fixed bounds needed for the next page.
type IncidentEpisodePage struct {
	Episodes     []IncidentEpisode `json:"episodes"`
	Since        time.Time         `json:"since"`
	Until        time.Time         `json:"until"`
	RetainedFrom time.Time         `json:"retained_from"`
	Before       string            `json:"before,omitempty"`
	Untrusted    Untrusted         `json:"untrusted"`
}

// IncidentEpisodes returns visible alert metadata through the same query for people and agents.
// The counting view contains no session references or command content.
func (b *Bridge) IncidentEpisodes(ctx context.Context, q IncidentEpisodeQuery) (IncidentEpisodePage, error) {
	now := b.now().UTC()
	filter, err := incidentEpisodeFilter(now, b.outcomeCutoff(now), q)
	if err != nil {
		return IncidentEpisodePage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, episodeReadBudget)
	defer cancel()
	rows, err := b.d.Store.IncidentEpisodeRecords(ctx, filter)
	if err != nil {
		return IncidentEpisodePage{}, err
	}
	page := IncidentEpisodePage{
		Episodes: make([]IncidentEpisode, 0, min(len(rows), filter.Limit)),
		Since:    filter.Since, Until: filter.Until, RetainedFrom: filter.RetainedFrom,
		Untrusted: untrusted("episodes[].title", "episodes[].services", "episodes[].participation[].by",
			"episodes[].agent_deliveries[].hook", "episodes[].agent_deliveries[].run.by",
			"episodes[].human_acknowledgement.by", "episodes[].verification.by", "episodes[].passing_check.by"),
	}
	if len(rows) > filter.Limit {
		rows = rows[:filter.Limit]
		page.Before = rows[len(rows)-1].ID
	}
	for _, row := range rows {
		var o store.IncidentOutcome
		if row.Archived != nil {
			o = *row.Archived
		} else {
			o = b.summariseEpisode(*row.Current)
		}
		page.Episodes = append(page.Episodes, episodeProjection(row, o))
	}
	return page, nil
}

func episodeProjection(row store.IncidentEpisodeRecord, o store.IncidentOutcome) IncidentEpisode {
	e := IncidentEpisode{
		Group: row.Group, Episode: row.Episode, OutcomeID: o.ID,
		State: "open", Title: o.Title, Services: o.Services, Severity: o.Severity,
		OpeningSeverity: o.OpeningSeverity, Origin: o.Origin, ExerciseRun: o.ExerciseRun,
		Started: o.Started, Resolved: tp(o.Resolved), Wake: "unknown",
		Participation: make([]store.Participation, 0, len(o.Participation)), HumanAcknowledgement: o.HumanAcknowledgement,
		Verification: o.Verification, PassingCheck: o.PassingCheck,
	}
	if e.Origin == "" {
		e.Origin = "unknown"
	}
	if e.OpeningSeverity == "" {
		e.OpeningSeverity = "unknown"
	}
	if e.Resolved != nil {
		e.State = "resolved"
	}
	for _, p := range o.Participation {
		// A provider's device observation does not identify a person who took part in the response.
		if p.Via != "pushover" {
			e.Participation = append(e.Participation, p)
		}
	}
	for _, d := range o.AgentDeliveries {
		if d.Event != "firing" {
			continue
		}
		e.AgentDeliveries = append(e.AgentDeliveries, d)
		if d.Status == "ok" {
			e.Wake = "accepted"
		}
	}
	return e
}

func incidentEpisodeFilter(now, retainedFrom time.Time, q IncidentEpisodeQuery) (store.IncidentEpisodeFilter, error) {
	limit := q.Limit
	if limit == 0 {
		limit = 50
	}
	key, validCursor := strings.CutPrefix(q.Before, store.EpisodeCursorPrefix)
	switch {
	case limit < 1 || limit > 200:
		return store.IncidentEpisodeFilter{}, fmt.Errorf("%w: limit is 1 to 200", ErrBadInput)
	case q.Before != "" && (!validCursor || !outcomeID.MatchString(key) || q.Since == "" || q.Until == ""):
		return store.IncidentEpisodeFilter{}, fmt.Errorf("%w: before requires a started episode cursor and the same since and until", ErrBadInput)
	case q.Group != "" && !outcomeGroup.MatchString(q.Group):
		return store.IncidentEpisodeFilter{}, fmt.Errorf("%w: group must be an alert group ID", ErrBadInput)
	case q.Severity != "" && !slices.Contains(config.Severities, q.Severity):
		return store.IncidentEpisodeFilter{}, fmt.Errorf("%w: severity is critical, warning or info", ErrBadInput)
	case !store.ValidOutcomeOrigin(q.Origin):
		return store.IncidentEpisodeFilter{}, fmt.Errorf("%w: origin is exercise or unknown", ErrBadInput)
	}
	until, err := incidentBound(q.Until, now)
	if err != nil {
		return store.IncidentEpisodeFilter{}, err
	}
	since, err := incidentBound(q.Since, until.Add(-24*time.Hour))
	if err != nil {
		return store.IncidentEpisodeFilter{}, err
	}
	if !since.Before(until) || until.After(now) || until.Sub(since) > maxEpisodeWindow {
		return store.IncidentEpisodeFilter{}, fmt.Errorf("%w: use a past start-time window of up to 31 days", ErrBadInput)
	}
	return store.IncidentEpisodeFilter{Since: since, Until: until, RetainedFrom: retainedFrom,
		Before: q.Before, Group: q.Group, Severity: q.Severity, Origin: q.Origin, Limit: limit}, nil
}

func incidentBound(value string, fallback time.Time) (time.Time, error) {
	if value == "" {
		return fallback, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: since and until must be RFC 3339 times", ErrBadInput)
	}
	return t.UTC(), nil
}
