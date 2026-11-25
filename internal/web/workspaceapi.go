package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The workspace API is the same-origin browser interface. It uses the console's
// session and never issues a bearer credential. The public REST and MCP
// interfaces keep their own authentication.
func (s *Server) registerWorkspaceAPI(at func(method, path string, h http.HandlerFunc)) {
	const root = "/api/v1"
	at("GET", root+"/session", s.workspace("", s.workspaceSession))
	at("GET", root+"/overview", s.workspace("status:read", s.workspaceOverview))
	at("GET", root+"/alerts", s.workspace("status:read", s.workspaceAlerts))
	at("GET", root+"/alerts/{id}", s.workspace("status:read", s.workspaceIncident))
	at("GET", root+"/alerts/{id}/episodes/{episode}", s.workspace("status:read", s.workspaceIncident))
	at("POST", root+"/alerts/{id}/ack", s.workspace("alerts:write", s.workspaceAck))
	at("POST", root+"/alerts/{id}/notes", s.workspace("alerts:write", s.workspaceNote))
	at("POST", root+"/alerts/{id}/probe-now", s.workspace("alerts:write", s.workspaceProbe))
}

func (s *Server) workspace(scope string, serve func(http.ResponseWriter, *http.Request, *viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		w.Header().Set("Cache-Control", "private, no-store")
		v := s.viewer(r)
		switch {
		case v == nil:
			workspaceError(w, http.StatusUnauthorized, "auth.required", "sign in to Beacon")
		case !sameOrigin(r):
			workspaceError(w, http.StatusForbidden, "auth.forbidden", "the request must be same-origin")
		case scope != "" && !v.has(scope):
			workspaceError(w, http.StatusForbidden, "auth.forbidden", "the account lacks the required permission")
		case r.Method == http.MethodPost && !equal(r.Header.Get("X-CSRF-Token"), v.Session.CSRF):
			workspaceError(w, http.StatusForbidden, "csrf.invalid", "refresh the workspace before submitting")
		default:
			serve(w, r, v)
		}
	}
}

func workspaceJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func workspaceError(w http.ResponseWriter, status int, code, message string) {
	workspaceJSON(w, status, map[string]string{"error": message, "code": code})
}

func (s *Server) workspaceSession(w http.ResponseWriter, r *http.Request, v *viewer) {
	sh := s.shellFor(r, v, "", "")
	workspaceJSON(w, http.StatusOK, struct {
		Language   string            `json:"language"`
		Name       string            `json:"name"`
		CSRF       string            `json:"csrf"`
		Docs       string            `json:"docs"`
		Navigation []navGroup        `json:"navigation"`
		Labels     map[string]string `json:"labels"`
	}{sh.Lang, sh.Who, v.Session.CSRF, sh.Project.Docs, sh.Nav, s.workspaceLabels()})
}

func (s *Server) workspaceLabels() map[string]string {
	return map[string]string{
		"overview": s.t("Overview"), "alerts": s.t("Alerts"), "account": s.t("Your account"), "docs": s.t("Documentation"),
		"menu": s.t("Menu"), "signOut": s.t("Sign out"), "skip": s.t("Skip to content"), "search": s.t("Search"),
		"attention": s.t("Needs attention"), "calm": s.t("Nothing needs attention"), "allAlerts": s.t("All alerts"),
		"item": s.t("Item"), "state": s.t("State"), "since": s.t("Since"), "filters": s.t("Filters"),
		"notifications": s.t("Critical notifications"), "openAlert": s.t("Open alert"), "upcoming": s.t("Coming up"),
		"noUpcoming": s.t("No maintenance is planned, and nothing expires in the next 30 days."),
		"sessions":   s.t("Sessions"), "changes": s.t("Changes"), "activity": s.t("Recent activity"),
		"noMatches": s.t("Nothing matches."), "noSessions": s.t("No machine sessions recorded yet."),
		"noChanges": s.t("No deployment or other change was reported yet."), "history": s.t("Incident history"),
		"title": s.t("Alert"), "severity": s.t("Severity"), "service": s.t("Service"),
		"prev": s.t("Previous page"), "next": s.t("Next page"), "close": s.t("Close"),
		"response": s.t("Alert response"), "timeline": s.t("Timeline"), "probes": s.t("Probes"), "notes": s.t("Notes"),
		"ack": s.t("Acknowledge"), "takeOver": s.t("Take over"), "probeNow": s.t("Probe again now"),
		"addNote": s.t("Add a note"), "note": s.t("Note"), "latestCheck": s.t("Latest on-demand check"),
		"checkWait": s.t("Allow up to 45 seconds for the results."), "none": s.t("None"),
		"deviceResponse": s.t("Pushover device acknowledged"), "silenced": s.t("Silenced"),
		"acknowledged": s.t("Acknowledged"), "refresh": s.t("Refresh status"),
		"loading": s.t("Loading workspace"), "loadError": s.t("The workspace could not load this page."),
		"changed": s.t("This incident has changed. Review the alert before taking action."),
		"failed":  s.t("That did not work; the log says why."), "saved": s.t("Saved"),
		"location": s.t("Location"), "result": s.t("Result"), "reason": s.t("Reason"), "target": s.t("Target"),
		"replay": s.t("Replay"), "openHistory": s.t("Open incident history"),
	}
}

func (s *Server) workspaceOverview(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := s.overviewData(r, v)
	workspaceJSON(w, http.StatusOK, struct {
		Health         health          `json:"health"`
		Metrics        overviewMetrics `json:"metrics"`
		Queue          []attentionItem `json:"queue"`
		Notifications  []deliveryRow   `json:"notifications"`
		DeliveryHint   string          `json:"deliveryHint"`
		DeliveryHelp   kvLink          `json:"deliveryHelp"`
		DeliveryDrills []kvLink        `json:"deliveryDrills"`
		Maintenance    kvLink          `json:"maintenance"`
		Upcoming       []feedItem      `json:"upcoming"`
		Sessions       []feedItem      `json:"sessions"`
		Changes        []feedItem      `json:"changes"`
		AddMonitor     kvLink          `json:"addMonitor"`
	}{
		Health: p.Health, Metrics: p.Stats, Queue: p.Attention, Notifications: p.Notifications, DeliveryHint: p.DeliveryHint,
		DeliveryHelp: p.DeliveryHelp, DeliveryDrills: p.DeliveryDrills, Maintenance: p.Maintenance,
		Upcoming: p.Coming, Sessions: p.Sessions, Changes: p.Changes, AddMonitor: p.AddMonitor,
	})
}

func (s *Server) workspaceAlerts(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := s.alertsData(r, v)
	workspaceJSON(w, http.StatusOK, struct {
		Alerts []alertRow   `json:"alerts"`
		Views  []facetValue `json:"views"`
		List   list         `json:"list"`
	}{p.Rows, p.Views, p.List})
}

// workspaceActions exposes form concurrency guards only when the current principal
// can take the corresponding action. Neither the template shell nor the store record
// is returned to the browser.
type workspaceActions struct {
	TakeOver, CanAck, CanNote, CanProbe bool
	AckVersion                          string
}

func (s *Server) workspaceIncident(w http.ResponseWriter, r *http.Request, v *viewer) {
	id, episode := r.PathValue("id"), r.PathValue("episode")
	if episode != "" && !link.ValidEpisode(episode) {
		workspaceError(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	g, ev, tl, err := s.Bridge.IncidentPageFor(r.Context(), id, episode, v.Principal)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) && episode != "" {
			if archived, err := s.Bridge.ArchivedIncidentID(id, episode); err == nil {
				workspaceJSON(w, http.StatusOK, struct {
					Archived string `json:"archived"`
				}{outcomePath(archived)})
				return
			}
		}
		s.workspaceOperationError(w, err)
		return
	}
	a := access{OK: true, Viewer: v, Principal: v.Principal, SessionDetail: v.hasAny("sessions:read|machines:ssh")}
	p := s.alertPage(r, ev, tl, g, a)
	var actions workspaceActions
	if p.Actions != nil {
		actions = workspaceActions{p.Actions.TakeOver, p.Actions.Ack != "", p.Actions.Note != "", p.Actions.Probe != "", p.Actions.AckVersion}
	}
	workspaceJSON(w, http.StatusOK, struct {
		ID, Episode, Title, Description, Status, StatusTone, Severity, SeverityTone, When string
		Ownership, Escalation, Phone, Delivery, Hook, ProbesError, Window, Legacy         string
		Actions                                                                           workspaceActions
		Machines                                                                          *alertMachineLinks
		Events                                                                            []eventRow
		Probes                                                                            []probeRow
		Sessions                                                                          []sessionRow
		Notes                                                                             []noteRow
		LatestCheck                                                                       *probeEvent
		Detail                                                                            workspaceIncidentDetails
	}{
		ID: id, Episode: g.Episode, Title: p.Heading, Description: p.Description, Status: p.Status,
		StatusTone: p.StatusClass, Severity: p.Severity, SeverityTone: p.SeverityClass, When: p.When,
		Ownership: p.Ownership, Escalation: p.Escalation, Phone: p.PhoneAcknowledgement, Delivery: p.PeopleDelivery,
		Hook: p.HookState, ProbesError: p.ProbesError, Window: p.Window, Legacy: link.IncidentPath(id, g.Episode),
		Actions: actions, Machines: p.Machines, Events: p.Events, Probes: p.Probes, Sessions: p.Sessions, Notes: p.Notes, LatestCheck: p.LatestProbe,
		Detail: workspaceIncidentDetails{
			Labels: p.L, Related: p.Related, Changes: p.Changes, Agents: p.Agents, Channel: p.Channel, Paused: p.Paused,
			Runbook: p.Runbook, Dashboard: p.Dashboard, Share: p.ShareLinks, Services: p.Services,
			Feedback: p.Feedback, FeedbackTone: p.FeedbackTone, PhoneCancellation: p.PhoneCancellation,
			PhonePending: p.PhoneCancellationPending, Handover: p.Handover, LatestProbeHref: p.LatestProbeHref,
			AgentRun: p.AgentRun, Actions: p.Actions,
		},
	})
}

type workspaceIncidentDetails struct {
	Labels                                    labels
	Related                                   []relatedRow
	Changes                                   []changeRow
	Agents, Services                          []string
	Channel, Runbook, Dashboard, Share        string
	Feedback, FeedbackTone, PhoneCancellation string
	Paused, PhonePending                      bool
	Handover                                  handoverNote
	LatestProbeHref                           string
	AgentRun                                  *alertAgentRun
	Actions                                   *alertActions
}

type workspaceMutation struct {
	Episode    string `json:"episode"`
	AckVersion string `json:"ackVersion"`
	Text       string `json:"text"`
}

func readWorkspaceMutation(w http.ResponseWriter, r *http.Request) (workspaceMutation, bool) {
	input := workspaceMutation{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxForm))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !link.ValidEpisode(input.Episode) || decoder.Decode(new(any)) != io.EOF {
		workspaceError(w, http.StatusBadRequest, "bad_input", "an incident episode and valid JSON are required")
		return input, false
	}
	return input, true
}

func (s *Server) workspaceAck(w http.ResponseWriter, r *http.Request, v *viewer) {
	in, ok := readWorkspaceMutation(w, r)
	if !ok {
		return
	}
	if len(in.AckVersion) != 64 || len(in.Text) > 8000 {
		workspaceError(w, http.StatusBadRequest, "bad_input", "the current acknowledgement version and a valid note are required")
		return
	}
	// The acknowledgement version covers the episode as well as the current owner.
	_, err := s.Bridge.AckIfUnchanged(r.Context(), r.PathValue("id"), v.actor(), in.Text, in.AckVersion)
	s.workspaceOperationResult(w, err)
}

func (s *Server) workspaceNote(w http.ResponseWriter, r *http.Request, v *viewer) {
	in, ok := readWorkspaceMutation(w, r)
	if !ok {
		return
	}
	_, err := s.Bridge.AddIncidentNote(r.Context(), r.PathValue("id"), in.Episode, v.actor(), in.Text)
	s.workspaceOperationResult(w, err)
}

func (s *Server) workspaceProbe(w http.ResponseWriter, r *http.Request, v *viewer) {
	in, ok := readWorkspaceMutation(w, r)
	if !ok {
		return
	}
	_, err := s.Bridge.ProbeNow(r.Context(), v.actor(), bridge.ProbeNowInput{AlertID: r.PathValue("id"), Episode: &in.Episode})
	s.workspaceOperationResult(w, err)
}

func (s *Server) workspaceOperationResult(w http.ResponseWriter, err error) {
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.workspaceOperationError(w, err)
}

func (s *Server) workspaceOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bridge.ErrNotFound):
		workspaceError(w, http.StatusNotFound, "not_found", "incident not found")
	case errors.Is(err, bridge.ErrAckChanged), errors.Is(err, bridge.ErrEpisodeChanged), errors.Is(err, bridge.ErrNotFiring):
		workspaceError(w, http.StatusConflict, problemCode(err), "incident state changed")
	case errors.Is(err, bridge.ErrBadInput), errors.Is(err, bridge.ErrProbeNotFound):
		workspaceError(w, http.StatusBadRequest, "bad_input", "the incident or action input is invalid")
	case errors.Is(err, bridge.ErrForbidden):
		workspaceError(w, http.StatusForbidden, "auth.forbidden", "the account lacks the required permission")
	case errors.Is(err, bridge.ErrProbeUnavailable), errors.Is(err, bridge.ErrProbeBusy), errors.Is(err, bridge.ErrProbeNotRecorded):
		workspaceError(w, http.StatusServiceUnavailable, "probe_unavailable", "the check could not be completed and recorded")
	default:
		s.log().Error("workspace operation failed", "err", err)
		workspaceError(w, http.StatusInternalServerError, "failed", "the requested operation failed")
	}
}
