package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/bridge"
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
	Sessions                                                                                                    string
	Incident, Service, Severity, Started, Resolved, Verification, Participation, NoParticipants, NoVerification string
	First, Older, Empty, Audit, Alert, Clear, Group, Detail                                                     string
}

func (s *Server) outcomeLabels() outcomeLabels {
	return outcomeLabels{
		Incident: s.t("Incident"), Service: s.t("Service"), Severity: s.t("Severity"), Started: s.t("Started"), Resolved: s.t("Alert resolved"),
		Verification: s.t("Latest on-demand check"), Participation: s.t("Recorded participation"), NoParticipants: s.t("No participation recorded"),
		NoVerification: s.t("No on-demand check recorded"), First: s.t("Newest incidents"), Older: s.t("Older incidents"),
		Empty: s.t("No incident outcomes match."), Audit: s.t("Related audit entries"), Alert: s.t("Open alert details"),
		Clear: s.t("All incident outcomes"), Group: s.t("Alert group"),
		Detail:   s.t("This history records alert activity. Recorded participation alone does not identify what repaired the service."),
		Sessions: s.t("Related sessions"),
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
	L                   outcomeLabels
	Rows                []outcomeRow
	Group, First, Older string
}

func (s *Server) incidentOutcomes(w http.ResponseWriter, r *http.Request, v *viewer) {
	q := r.URL.Query()
	group, before := q.Get("group"), q.Get("before")
	result, err := s.Bridge.IncidentOutcomes(group, before, perPage)
	if err != nil {
		s.outcomeProblem(w, r, v, err)
		return
	}
	p := outcomesPage{shell: s.shellFor(r, v, "alerts", s.t("Incident history"), crumb{Label: s.t("Alerts"), Href: consolePath("/alerts")}),
		L: s.outcomeLabels(), Group: group}
	base := consolePath("/incident-outcomes")
	params := url.Values{}
	if group != "" {
		params.Set("group", group)
	}
	if before != "" {
		p.First = href(base, params)
	}
	if result.Before != "" {
		params.Set("before", result.Before)
		p.Older = href(base, params)
	}
	for _, o := range result.Outcomes {
		p.Rows = append(p.Rows, s.outcomeRow(o))
	}
	s.render(w, outcomesTmpl, p)
}

type outcomePage struct {
	shell
	L                   outcomeLabels
	Incident            outcomeRow
	Audit, Alert, Group string
	By, Via, Checked    string
	Participation       []participantRow
	Sessions            []sessionItem
}

type participantRow struct{ By, Via, Kind, Action, At, ISO string }

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
	if g, err := s.Bridge.Group(o.Group); err == nil && g.ResolvedAt != nil && g.ResolvedAt.Equal(o.Resolved) && g.StartsAt.Equal(o.Started) && o.AuditUntil.IsZero() {
		p.Alert = link.AlertPath(o.Group)
	}
	if check := o.Verification; check != nil {
		p.By, p.Via, p.Checked = check.By, s.via(check.Via), s.clock(check.At)
	}
	for _, a := range o.Participation {
		p.Participation = append(p.Participation, participantRow{
			At: s.clock(a.At), ISO: iso(a.At), By: a.By, Via: s.via(a.Via),
			Kind: s.participantKind(a.Kind), Action: s.participationAction(a.Action),
		})
	}
	if s.SSH != nil && v.hasAny("sessions:read|machines:ssh") {
		for _, id := range o.Sessions {
			if rec, ok := s.Store.Session(id); ok {
				p.Sessions = append(p.Sessions, s.sessionItem(rec))
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
	f := store.AuditFilter{Target: o.Group, From: o.Started, To: o.AuditUntil, Limit: perPage}
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
	p.Count, p.Rows = num(total), s.auditRows(entries)
	s.render(w, auditTmpl, p)
}
