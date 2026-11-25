package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

var incidentEpisodesTmpl = parse("incident-episodes.html")

const episodeInputTime = "2006-01-02T15:04:05"

type incidentEpisodesPage struct {
	shell                                                                                 `json:"-"`
	L                                                                                     outcomeLabels
	Rows                                                                                  []incidentEpisodeRow
	Filters                                                                               []outcomeFacet
	Group, Since, Until, Before, History, Coverage, Status, SinceLabel, UntilLabel, Empty string
	HistoryLabel                                                                          string
}

type incidentEpisodeRow struct {
	Title, Href, Services, Severity, SeverityTone, OpeningUnknown, Origin string
	State, Tone, Started, Resolved, Wake                                  string
	Programmes                                                            []alertAgentRun
	Human, Passing                                                        string
	Participants                                                          []string
}

func (s *Server) incidentEpisodes(w http.ResponseWriter, r *http.Request, v *viewer) {
	values := r.URL.Query()
	q := bridge.IncidentEpisodeQuery{
		Since: episodeDateInput(values.Get("since")), Until: episodeDateInput(values.Get("until")),
		Group: values.Get("group"), Severity: values.Get("severity"), Origin: values.Get("origin"),
		Before: values.Get("before"), Limit: perPage,
	}
	if q.Before == "" && q.Until == "" {
		q.Until = s.now().UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	result, err := s.Bridge.IncidentEpisodes(r.Context(), q)
	if err != nil {
		s.outcomeProblem(w, r, v, err)
		return
	}
	selection := url.Values{"group": {q.Group}, "severity": {q.Severity}, "origin": {q.Origin}}
	p := incidentEpisodesPage{
		shell: s.shellFor(r, v, "incident-outcomes", s.t("Started episodes"),
			crumb{Label: s.t("Incident history"), Href: consolePath("/incident-outcomes")}),
		L: s.outcomeLabels(), Group: q.Group, Status: s.t("Status"),
		Since: result.Since.UTC().Format(episodeInputTime), Until: result.Until.UTC().Format(episodeInputTime),
		SinceLabel: s.t("Started at or after (UTC)"), UntilLabel: s.t("Started before (UTC)"),
		History: href(consolePath("/incident-outcomes"), selection), HistoryLabel: s.t("Resolved outcomes"),
		Coverage: s.t("Resolved outcomes retained from %s.", s.clock(result.RetainedFrom)),
		Empty:    s.t("No episodes started in this period."),
	}
	severities := []option{{Label: s.t("All severities")}}
	for _, severity := range config.Severities {
		severities = append(severities, option{Value: severity, Label: s.severity(severity)})
	}
	p.Filters = []outcomeFacet{
		{Name: "severity", Label: p.L.Severity, Options: outcomeOptions(q.Severity, severities...)},
		{Name: "origin", Label: p.L.Origin, Options: outcomeOptions(q.Origin, option{Label: s.t("All origins")},
			option{Value: store.ExerciseOrigin, Label: p.L.Exercise}, option{Value: "unknown", Label: p.L.UnknownOrigin})},
	}
	if result.Before != "" {
		selection.Set("since", result.Since.Format(time.RFC3339Nano))
		selection.Set("until", result.Until.Format(time.RFC3339Nano))
		selection.Set("before", result.Before)
		p.Before = href(consolePath("/incident-episodes"), selection)
	}
	for _, e := range result.Episodes {
		p.Rows = append(p.Rows, s.incidentEpisodeRow(e, p.L))
	}
	s.render(w, incidentEpisodesTmpl, p)
}

func episodeDateInput(value string) string {
	for _, layout := range []string{episodeInputTime, "2006-01-02T15:04"} {
		if when, err := time.Parse(layout, value); err == nil {
			return when.Format(time.RFC3339)
		}
	}
	return value
}

func (s *Server) incidentEpisodeRow(e bridge.IncidentEpisode, l outcomeLabels) incidentEpisodeRow {
	row := incidentEpisodeRow{
		Title: e.Title, Href: link.IncidentPath(e.Group, e.Episode), Services: strings.Join(e.Services, ", "),
		State: s.t("Still open"), Tone: "warning",
		Started: s.clock(e.Started), Origin: l.UnknownOrigin,
		Wake: s.t("Hook acceptance unknown"), Human: l.NoHumanAck, Passing: l.NoPassingCheck,
	}
	if e.OutcomeID != "" {
		row.Href = outcomePath(e.OutcomeID)
	}
	severity := e.OpeningSeverity
	if severity == "unknown" {
		severity = e.Severity
		row.OpeningUnknown = s.t("Opening severity unknown")
	}
	row.Severity, row.SeverityTone = s.severity(severity), tone(severity)
	if e.Resolved != nil {
		row.State, row.Tone, row.Resolved = l.Resolved, "success", s.clock(*e.Resolved)
	}
	if e.Origin == store.ExerciseOrigin {
		row.Origin = l.Exercise
	}
	if e.Wake == "accepted" {
		row.Wake = s.t("Accepted by hook")
	}
	for _, d := range e.AgentDeliveries {
		if d.Run != nil {
			row.Programmes = append(row.Programmes, s.agentRun(bridge.Event{By: d.Hook, Event: d.Event, Result: d.Run.Status, Exit: d.Run.ExitCode}))
		}
	}
	if e.HumanAcknowledgement != nil {
		row.Human = l.HumanAck
	}
	if e.PassingCheck != nil {
		row.Passing = l.CheckRecorded
		if e.PassingCheck.ActorKind == "agent" {
			row.Passing = l.AgentPassingCheck
		}
	}
	for _, participant := range e.Participation {
		row.Participants = append(row.Participants, s.participantKind(participant.Kind))
	}
	return row
}
