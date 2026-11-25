package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

var (
	outcomesTmpl = parse("incident-outcomes.html")
	outcomeTmpl  = parse("incident-outcome.html")
)

func outcomePath(id string) string {
	return consolePath("/incident-outcomes/") + url.PathEscape(id)
}

type outcomeLabels struct {
	Sessions, PassingCheck                                                                                      string
	Deliveries, Delivery, Programme, NoProgramme, ProgrammeStart, ProgrammeReport                               string
	Incident, Service, Severity, Started, Resolved, Verification, Participation, NoParticipants, NoVerification string
	First, Older, Empty, Audit, Alert, Group, Detail                                                            string
	Evidence, Apply, Reset, AllGroups, Acknowledgement                                                          string
	AgentReport, NoAgentReport, HumanAck, NoHumanAck, CheckRecorded, NoPassingCheck                             string
}

func (s *Server) outcomeLabels() outcomeLabels {
	return outcomeLabels{
		Incident: s.t("Incident"), Service: s.t("Service"), Severity: s.t("Severity"), Started: s.t("Started"), Resolved: s.t("Alert resolved"),
		Verification: s.t("Latest on-demand check"), Participation: s.t("Recorded participation"), NoParticipants: s.t("No participation recorded"),
		PassingCheck:   s.t("Passing check near rule recovery"),
		NoVerification: s.t("No on-demand check recorded"), First: s.t("Newest incidents"), Older: s.t("Older incidents"),
		Empty: s.t("No incident outcomes match."), Audit: s.t("Related audit entries"), Alert: s.t("Open alert details"),
		Group:    s.t("Alert group"),
		Detail:   s.t("This history records alert activity. Recorded participation alone does not identify what repaired the service."),
		Sessions: s.t("Related sessions"), Deliveries: s.t("Agent deliveries"),
		Delivery: s.t("Hook delivery"), Programme: s.t("Programme report"),
		NoProgramme: s.t("No Agent program report recorded."), ProgrammeStart: s.t("Started"),
		ProgrammeReport: s.t("Reported"),
		Evidence:        s.t("Evidence"), Apply: s.t("Apply filters"), Reset: s.t("Clear filters"), AllGroups: s.t("All alert groups"),
		Acknowledgement: s.t("Human acknowledgement"),
		AgentReport:     s.t("Agent trigger report recorded"), NoAgentReport: s.t("No Agent trigger report recorded"),
		HumanAck: s.t("Human acknowledgement recorded"), NoHumanAck: s.t("No human acknowledgement recorded"),
		CheckRecorded: s.t("Passing check recorded"), NoPassingCheck: s.t("No passing check recorded"),
	}
}

type outcomeRow struct {
	Title, Href, Service, Severity, SeverityTone, Started, Resolved, Duration, Verification string
	Participants                                                                            []string
}

func (s *Server) outcomeRow(o store.IncidentOutcome) outcomeRow {
	row := outcomeRow{
		Title: o.Title, Href: outcomePath(o.ID), Service: strings.Join(o.Services, ", "), Severity: s.severity(o.Severity),
		SeverityTone: tone(o.Severity), Started: s.clock(o.Started), Resolved: s.clock(o.Resolved), Duration: s.dur(o.Resolved.Sub(o.Started)),
	}
	for _, p := range o.Participation {
		row.Participants = append(row.Participants, s.participantKind(p.Kind))
	}
	if v := o.Verification; v != nil {
		row.Verification = s.t("%d passed, %d failed, %d missing", v.Passed, v.Failed, v.Missing)
	}
	return row
}

func (s *Server) participantKind(kind string) string {
	switch kind {
	case "person":
		return s.t("Person")
	case "agent":
		return s.t("Agent")
	default:
		return s.t("Unattributed")
	}
}

type outcomesPage struct {
	shell
	L                                     outcomeLabels
	Rows                                  []outcomeHistoryRow
	Filters                               []outcomeFacet
	Group, AllGroups, Reset, First, Older string
}

type outcomeHistoryRow struct {
	outcomeRow
	AgentReport, HumanAck, PassingCheck bool
}

type outcomeFacet struct {
	Name, Label string
	Options     []option
}

func outcomeOptions(selected string, options ...option) []option {
	for i := range options {
		options[i].Selected = options[i].Value == selected
	}
	return options
}

func (s *Server) outcomeFilters(l outcomeLabels, q bridge.OutcomeQuery) []outcomeFacet {
	severities := []option{{Label: s.t("All severities")}}
	for _, value := range config.Severities {
		severities = append(severities, option{Value: value, Label: s.severity(value)})
	}
	dates := []option{
		{Label: s.t("All retained incidents")},
		{Value: "7d", Label: s.t("Last 7 days")},
		{Value: "30d", Label: s.t("Last 30 days")},
	}
	if start, err := time.Parse(time.RFC3339, q.Since); err == nil {
		dates = append(dates, option{Value: q.Since, Label: s.t("Resolved since %s", s.clock(start))})
	} else if q.Since != "" && q.Since != "7d" && q.Since != "30d" {
		dates = append(dates, option{Value: q.Since, Label: q.Since})
	}
	return []outcomeFacet{
		{Name: "severity", Label: l.Severity, Options: outcomeOptions(q.Severity, severities...)},
		{Name: "since", Label: s.t("Resolved within"), Options: outcomeOptions(q.Since, dates...)},
		{Name: "evidence", Label: l.Evidence, Options: outcomeOptions(q.Evidence,
			option{Label: s.t("Any evidence state")},
			option{Value: "agent_report", Label: l.AgentReport},
			option{Value: "no_agent_report", Label: l.NoAgentReport},
			option{Value: "human_ack", Label: l.HumanAck},
			option{Value: "no_human_ack", Label: l.NoHumanAck},
			option{Value: "passing_check", Label: l.CheckRecorded},
			option{Value: "no_passing_check", Label: l.NoPassingCheck})},
	}
}

func (s *Server) incidentOutcomes(w http.ResponseWriter, r *http.Request, v *viewer) {
	values := r.URL.Query()
	q := bridge.OutcomeQuery{
		Group: values.Get("group"), Before: values.Get("before"), Severity: values.Get("severity"),
		Since: strings.TrimSpace(values.Get("since")), Evidence: values.Get("evidence"), Limit: perPage,
	}
	result, err := s.Bridge.IncidentOutcomesFor(v.Principal, q)
	if err != nil {
		s.outcomeProblem(w, r, v, err)
		return
	}
	p := outcomesPage{shell: s.shellFor(r, v, "alerts", s.t("Incident history"), crumb{Label: s.t("Alerts"), Href: consolePath("/alerts")}),
		L: s.outcomeLabels(), Group: q.Group}
	p.Filters = s.outcomeFilters(p.L, q)
	base := consolePath("/incident-outcomes")
	params := url.Values{}
	for name, value := range map[string]string{"severity": q.Severity, "since": q.Since, "evidence": q.Evidence} {
		if value != "" {
			params.Set(name, value)
		}
	}
	if q.Group != "" {
		p.AllGroups = href(base, params)
		p.Reset = href(base, url.Values{"group": {q.Group}})
	} else {
		p.Reset = base
	}
	if len(params) == 0 {
		p.Reset = ""
	}
	if q.Group != "" {
		params.Set("group", q.Group)
	}
	if q.Before != "" {
		p.First = href(base, params)
	}
	if result.Before != "" {
		params.Set("before", result.Before)
		p.Older = href(base, params)
	}
	for _, o := range result.Outcomes {
		p.Rows = append(p.Rows, outcomeHistoryRow{
			outcomeRow: s.outcomeRow(o), AgentReport: o.AgentReportRecorded(),
			HumanAck: o.HasHumanAcknowledgement(), PassingCheck: o.HasPassingCheck(),
		})
	}
	s.render(w, outcomesTmpl, p)
}

type outcomePage struct {
	shell
	L                    outcomeLabels
	Incident             outcomeRow
	Audit, Alert, Group  string
	Latest, PassingCheck *outcomeCheck
	HumanAck             *participantRow
	Participation        []participantRow
	Sessions             []sessionItem
	Deliveries           []outcomeDeliveryRow
}

type participantRow struct{ By, Via, Kind, Action, At, ISO string }

type outcomeCheck struct{ Summary, By, Via, Kind, At, ISO, Phase string }
type outcomeTime struct{ Text, ISO string }
type outcomeDeliveryRow struct {
	Hook, Event, Status, Tone string
	Delivery, Started, Report outcomeTime
	Run                       *alertAgentRun
}

func (s *Server) outcomeTime(t time.Time) outcomeTime {
	if t.IsZero() {
		return outcomeTime{}
	}
	return outcomeTime{Text: s.clock(t), ISO: iso(t)}
}

func (s *Server) outcomeDelivery(d store.OutcomeAgentDelivery, archived bool) outcomeDeliveryRow {
	row := outcomeDeliveryRow{Hook: d.Hook, Delivery: s.outcomeTime(d.DoneAt)}
	switch d.Event {
	case "firing":
		row.Event = s.t("Alert trigger")
	case "resolved":
		row.Event = s.t("Recovery notice")
	}
	switch d.Status {
	case "ok":
		row.Status, row.Tone = s.t("Accepted by hook"), "success"
	case "failed":
		row.Status, row.Tone = s.t("Failed"), "danger"
	case "pending":
		row.Status, row.Tone = s.t("Pending"), "warning"
		if archived {
			row.Status = s.t("Pending at archive")
		}
		row.Delivery = s.outcomeTime(d.QueuedAt)
	}
	if d.Run != nil {
		run := s.agentRun(bridge.Event{By: d.Hook, Event: d.Event, Result: d.Run.Status, Exit: d.Run.ExitCode})
		row.Run, row.Report = &run, s.outcomeTime(d.Run.At)
		if d.Run.Status != "running" {
			row.Started = s.outcomeTime(d.StartedAt)
		} else if archived {
			run.Status = s.t("Last reported running")
		}
	}
	return row
}

func (s *Server) outcomeCheck(v *store.Verification, resolved time.Time) *outcomeCheck {
	if v == nil {
		return nil
	}
	check := &outcomeCheck{
		Summary: s.t("%d passed, %d failed, %d missing", v.Passed, v.Failed, v.Missing),
		By:      v.By, Via: s.via(v.Via),
	}
	if v.ActorKind != "" {
		check.Kind = s.participantKind(v.ActorKind)
	}
	if v.At.IsZero() {
		return check
	}
	check.At, check.ISO = s.clock(v.At), iso(v.At)
	switch v.At.Compare(resolved) {
	case -1:
		check.Phase = s.t("Completed before rule recovery")
	case 0:
		check.Phase = s.t("Completed at rule recovery")
	case 1:
		check.Phase = s.t("Completed after rule recovery")
	}
	return check
}

func (s *Server) incidentOutcome(w http.ResponseWriter, r *http.Request, v *viewer) {
	result, err := s.Bridge.IncidentOutcome(r.PathValue("id"))
	if err != nil {
		s.outcomeProblem(w, r, v, err)
		return
	}
	o := result.Outcome
	p := outcomePage{
		shell: s.shellFor(r, v, "alerts", o.Title, crumb{Label: s.t("Incident history"), Href: consolePath("/incident-outcomes")}),
		L:     s.outcomeLabels(), Incident: s.outcomeRow(o), Group: o.Group,
		Audit: consolePath("/audit") + "?outcome=" + url.QueryEscape(o.ID),
	}
	if problem := r.URL.Query().Get("error"); problem == "incident_changed" || problem == "ack_changed" {
		p.Problem = s.t("This incident has changed. Review the alert before taking action.")
	}
	if g, err := s.Bridge.Group(o.Group); err == nil && g.ResolvedAt != nil && g.ResolvedAt.Equal(o.Resolved) && g.StartsAt.Equal(o.Started) && o.AuditUntil.IsZero() {
		p.Alert = link.IncidentPath(o.Group, g.Episode)
	}
	p.Latest = s.outcomeCheck(o.Verification, o.Resolved)
	if o.Verification == nil || (o.PassingCheck != nil && o.Verification.ID != o.PassingCheck.ID) {
		p.PassingCheck = s.outcomeCheck(o.PassingCheck, o.Resolved)
	}
	if ack := o.HumanAcknowledgement; ack != nil {
		p.HumanAck = &participantRow{By: ack.By, Via: s.via(ack.Via), At: s.clock(ack.At), ISO: iso(ack.At)}
	}
	for _, a := range o.Participation {
		p.Participation = append(p.Participation, participantRow{
			At: s.clock(a.At), ISO: iso(a.At), By: a.By, Via: s.via(a.Via),
			Kind: s.participantKind(a.Kind), Action: s.participationAction(a.Action),
		})
	}
	for _, d := range o.AgentDeliveries {
		p.Deliveries = append(p.Deliveries, s.outcomeDelivery(d, !o.AuditUntil.IsZero()))
	}
	permit := s.Store.SessionAccess(v.Principal)
	if s.SSH != nil && permit.Details() {
		for _, id := range o.Sessions {
			if rec, ok := s.Store.Session(id); ok && permit.Visible(rec) {
				p.Sessions = append(p.Sessions, s.sessionItem(rec, true))
			}
		}
	}
	s.render(w, outcomeTmpl, p)
}

func (s *Server) participationAction(action string) string {
	switch action {
	case "ack":
		return s.t("Acknowledged")
	case "silence":
		return s.t("Silenced")
	case "note":
		return s.t("Added a note")
	case "probe_now":
		return s.t("Ran an on-demand check")
	}
	return ""
}

func (s *Server) outcomeProblem(w http.ResponseWriter, r *http.Request, v *viewer, err error) {
	switch {
	case errors.Is(err, bridge.ErrOutcomeNotFound):
		s.missing(w, r, v)
	case errors.Is(err, bridge.ErrBadInput):
		s.message(w, r, v, http.StatusBadRequest, s.t("Incident history"), s.t("This incident history link is invalid."))
	default:
		s.log().Error("read incident outcomes", "err", err)
		s.message(w, r, v, http.StatusInternalServerError, s.t("Incident history"), s.t("Incident history could not be loaded."))
	}
}

type auditPage struct {
	listPage[auditRow]
	IncidentHref, IncidentTitle string
}

func (s *Server) auditRows(entries []store.AuditEntry) []auditRow {
	out := make([]auditRow, 0, len(entries))
	for _, e := range entries {
		out = append(out, auditRow{Time: s.clock(e.Time), Actor: e.Actor, Via: s.via(e.Via), Action: e.Action, Target: e.Target, Detail: e.Detail})
	}
	return out
}

func (s *Server) outcomeAudit(w http.ResponseWriter, r *http.Request, v *viewer, id string) {
	if !v.has("status:read") {
		s.forbidden(w, r, v, "status:read")
		return
	}
	result, err := s.Bridge.IncidentOutcome(id)
	if err != nil {
		s.outcomeProblem(w, r, v, err)
		return
	}
	o := result.Outcome
	f := store.AuditFilter{Target: o.Group, ExactRelatedTarget: o.ID, From: o.Started, To: o.AuditUntil, Limit: perPage}
	if f.To.IsZero() {
		f.To = s.now()
	}
	q := r.URL.Query()
	requested, _ := strconv.Atoi(q.Get("page"))
	// Avoid overflowing the offset on an untrusted page number. An excessive page falls back to the
	// last page after the query reports the total.
	f.Offset = (max(1, min(requested, 1_000_000)) - 1) * perPage
	entries, total, err := s.Bridge.AuditQuery(f)
	if err != nil {
		s.outcomeProblem(w, r, v, err)
		return
	}
	p := auditPage{listPage: listPage[auditRow]{
		shell: s.shellFor(r, v, "audit", s.t("Related audit entries"), crumb{Label: s.t("Incident history"), Href: consolePath("/incident-outcomes")}),
		L:     s.listLabels(), List: list{Empty: s.t("No related audit entries are retained.")},
	}, IncidentHref: outcomePath(o.ID), IncidentTitle: o.Title}
	_, from, _ := s.page(total, q, consolePath("/audit"), &p.List)
	if from != f.Offset && total > 0 {
		f.Offset = from
		entries, _, err = s.Bridge.AuditQuery(f)
		if err != nil {
			s.outcomeProblem(w, r, v, err)
			return
		}
	}
	s.Bridge.FilterSessionAudit(v.Principal, entries)
	p.Count, p.Rows = num(total), s.auditRows(entries)
	s.render(w, auditTmpl, p)
}
