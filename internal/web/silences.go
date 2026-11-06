package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// What keeps alerts from paging people: the silences in force or to come, and the maintenance windows,
// planned, in progress and past. A silence is made on its alert's page (or by an agent, with
// silence_alert); a window is planned with schedule_maintenance or the API, ahead of time, for the
// status pages to announce. Here either is ended early: a silence by whoever may silence, a window by
// whoever may plan one. A window's silences end with it, so they are ended through the window.

// maxPastWindows is how many of the windows that ended the page lists.
const maxPastWindows = 20

type silencesLabels struct {
	Windows, Silences, Title, Services, Starts, Ends, Status, By, Matches, Comment, Alert, State string
	NoWindows, NoSilences, WindowsHint, SilencesHint, EndNow, CallOff, End, WithWindow, Problem  string
}

func (s *Server) silencesLabels() silencesLabels {
	return silencesLabels{
		Windows: s.t("Maintenance windows"), Silences: s.t("Silences"), Title: s.t("Title"), Services: s.t("Services"),
		Starts: s.t("Starts"), Ends: s.t("Ends"), Status: s.t("Status"), By: s.t("By"), Matches: s.t("Matches"),
		Comment: s.t("Comment"), Alert: s.t("Alert"), State: s.t("State"),
		NoWindows:    s.t("No maintenance is planned."),
		NoSilences:   s.t("Nothing is silenced."),
		WindowsHint:  s.t("Plan maintenance ahead of time with schedule_maintenance or POST /api/v1/maintenance: the status pages announce it, and its services page nobody while it lasts."),
		SilencesHint: s.t("Silence an alert from its page, for an hour to a day, saying why; every alert of its group stays quiet until the silence ends."),
		EndNow:       s.t("End now"), CallOff: s.t("Call off"), End: s.t("End the silence"),
		WithWindow: s.t("Ends with its maintenance window"),
		Problem:    s.t("The silences could not be read from Alertmanager"),
	}
}

type windowItem struct {
	ID, Title, Description, Services, Starts, Ends, Status, Tone, By, Via, End, EndLabel string
}

type silenceItem struct {
	ID, Matches, Comment, By, Starts, Ends, Left, State, Tone, Alert, AlertHref, Window, End string
}

type silencesPage struct {
	shell
	L        silencesLabels
	Windows  []windowItem
	Silences []silenceItem
	Unread   string // why the silences could not be read
}

var silencesTmpl = parse("silences.html")

// plannedWindows counts the maintenance windows in progress or to come, for the sidebar.
func (s *Server) plannedWindows() int {
	n, now := 0, s.now()
	for _, m := range s.Store.MaintenanceWindows() {
		if !m.Cancelled && m.End().After(now) {
			n++
		}
	}
	return n
}

// windowState is a maintenance window's state in words, its tone, and whether it is over.
func (s *Server) windowState(m store.Maintenance, now time.Time) (string, string, bool) {
	switch {
	case m.Cancelled:
		return s.t("Called off"), "", true
	case !m.End().After(now):
		return s.t("Completed"), "success", true
	case !m.StartsAt.After(now):
		return s.t("In progress"), "info", false
	}
	return s.t("Scheduled"), "warning", false
}

func (s *Server) silencesPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := silencesPage{shell: s.shellFor(r, v, "silences", s.t("Silences and maintenance")), L: s.silencesLabels()}
	now := s.now()
	ws := s.Bridge.MaintenanceWindows()
	// the windows in progress, then those to come, soonest first, then those that ended, latest first
	var current, past []windowItem
	byWindow := map[string]string{}
	slices.SortStableFunc(ws, func(a, b store.Maintenance) int { return a.StartsAt.Compare(b.StartsAt) })
	for _, m := range ws {
		state, tn, over := s.windowState(m, now)
		it := windowItem{ID: m.ID, Title: m.Title, Description: m.Description, Services: strings.Join(m.Services, ", "),
			Starts: s.short(m.StartsAt), Ends: s.short(m.End()), Status: state, Tone: tn, By: m.CreatedBy, Via: s.via(m.Via)}
		if over {
			past = append(past, it)
			continue
		}
		for _, id := range m.Silences {
			byWindow[id] = m.Title
		}
		if v.has("maintenance:write") {
			it.End = consolePath("/maintenance/") + url.PathEscape(m.ID) + "/end"
			it.EndLabel = p.L.EndNow
			if m.StartsAt.After(now) {
				it.EndLabel = p.L.CallOff
			}
		}
		current = append(current, it)
	}
	slices.Reverse(past)
	if len(past) > maxPastWindows {
		past = past[:maxPastWindows]
	}
	p.Windows = append(current, past...)

	ss, err := s.Bridge.Silences(r.Context())
	if err != nil {
		s.log().Error("console: reading the silences failed", "err", err)
		p.Unread = p.L.Problem
	}
	alerts := map[string]bridge.GroupView{}
	for _, g := range s.Bridge.Groups(false) {
		if g.Silence != nil {
			alerts[g.Silence.ID] = g
		}
	}
	for _, x := range ss {
		if x.Status.State == "expired" {
			continue
		}
		it := silenceItem{ID: x.ID, Matches: matchers(x.Matchers), Comment: x.Comment, By: x.CreatedBy, Starts: s.short(x.StartsAt),
			Ends: s.short(x.EndsAt), State: s.t("Active"), Tone: "info"}
		if x.Status.State == "pending" {
			it.State, it.Tone = s.t("Pending"), "warning"
		} else {
			it.Left = s.t("%s left", s.dur(x.EndsAt.Sub(now)))
		}
		if g, ok := alerts[x.ID]; ok {
			it.Alert, it.AlertHref = g.Title, link.AlertPath(g.ID)
		}
		switch {
		case byWindow[x.ID] != "" || strings.HasPrefix(x.Comment, bridge.MaintenancePrefix):
			it.Window = p.L.WithWindow
		case v.has("alerts:write"):
			it.End = consolePath("/silences/") + url.PathEscape(x.ID) + "/expire"
		}
		p.Silences = append(p.Silences, it)
	}
	p.Count = s.t("%d in force or planned", len(p.Silences)+len(current))
	s.render(w, silencesTmpl, p)
}

// matchers says what a silence matches, as Alertmanager writes it: service="git", host=~"edge-.*".
func matchers(ms []alertmanager.Matcher) string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		op := "="
		switch {
		case m.IsRegex && !m.IsEqual:
			op = "!~"
		case m.IsRegex:
			op = "=~"
		case !m.IsEqual:
			op = "!="
		}
		out = append(out, m.Name+op+`"`+m.Value+`"`)
	}
	return strings.Join(out, ", ")
}

// expireSilence ends a silence, and goes back to the page the form was on: the alert's or the list.
func (s *Server) expireSilence(w http.ResponseWriter, r *http.Request, v *viewer) {
	page := consolePath("/silences")
	if b := local(r.PostForm.Get("back")); strings.HasPrefix(b, consolePath("/")) {
		page = b
	}
	if err := s.Bridge.ExpireSilence(r.Context(), r.PathValue("id"), v.actor()); err != nil {
		s.failed(w, r, page, "expire_silence", err)
		return
	}
	back(w, r, page, "done", "unsilenced")
}

// endMaintenance ends a maintenance window now, or calls it off before it began.
func (s *Server) endMaintenance(w http.ResponseWriter, r *http.Request, v *viewer) {
	page := consolePath("/silences")
	m, err := s.Bridge.EndMaintenance(r.Context(), r.PathValue("id"), v.actor())
	switch {
	case errors.Is(err, bridge.ErrNoMaintenance):
		s.missing(w, r, v)
	case errors.Is(err, bridge.ErrMaintenanceEnded):
		back(w, r, page, "error", "window_over")
	case err != nil:
		s.failed(w, r, page, "end_maintenance", err)
	case m.Cancelled:
		back(w, r, page, "done", "called_off")
	default:
		back(w, r, page, "done", "maintenance_ended")
	}
}
