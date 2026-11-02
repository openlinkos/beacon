package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The console's lists: alerts, sessions, agent calls, the audit log and change events, each with its
// facets, newest first, 50 to a page, restacked by the design system on a narrow screen.

// listLabels are the column headings and the words of the lists.
type listLabels struct {
	Title, Severity, State, Service, Since, Acked, Silenced, Started, Agent, Machine, Kind, Via, Command, Duration, Exit string
	Hook, Event, Status, Code, Attempts, When, Alert, Actor, Action, Target, Detail, Time, Source, Summary, Yes, No      string
	Open, Target2, Configured, Calls, Severities, Resolved, NoHooks                                                      string
}

func (s *Server) listLabels() listLabels {
	return listLabels{
		Title: s.t("Alert"), Severity: s.t("Severity"), State: s.t("State"), Service: s.t("Service"), Since: s.t("Since"),
		Acked: s.t("Acknowledged"), Silenced: s.t("Silenced"), Started: s.t("Started"), Agent: s.t("Agent"), Machine: s.t("Machine"),
		Kind: s.t("Kind"), Via: s.t("Via"), Command: s.t("Command"), Duration: s.t("Duration"), Exit: s.t("Exit status"),
		Hook: s.t("Hook"), Event: s.t("Event"), Status: s.t("Status"), Code: s.t("HTTP status"), Attempts: s.t("Attempts"), When: s.t("When"),
		Alert: s.t("Alert"), Actor: s.t("Who"), Action: s.t("Action"), Target: s.t("Target"), Detail: s.t("Detail"), Time: s.t("Time"),
		Source: s.t("Source"), Summary: s.t("Summary"), Yes: s.t("Yes"), No: s.t("No"), Open: s.t("Still open"),
		Target2: s.t("Calls"), Configured: s.t("Hooks configured"), Calls: s.t("Calls made"), Severities: s.t("Severities"),
		Resolved: s.t("Also when it resolves"), NoHooks: s.t("No agent hooks are configured."),
	}
}

func (s *Server) yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (s *Server) yesNoLabel(v string) string {
	if v == "yes" {
		return s.t("Yes")
	}
	return s.t("No")
}

// ---- alerts

type alertRow struct {
	Title, Href, Severity, SeverityTone, State, StateTone, Service, Since, When string
	Acked, Silenced                                                             bool
}

type listPage[R any] struct {
	shell
	L    listLabels
	List list
	Rows []R
}

var alertsTmpl = parse("alerts.html")

func (s *Server) alerts(w http.ResponseWriter, r *http.Request, v *viewer) {
	now := s.now()
	groups := s.Bridge.Groups(false)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].StartsAt.After(groups[j].StartsAt) })
	silenced := func(g bridge.GroupView) bool { return g.Silence != nil && now.Before(g.Silence.Until) }
	facets := []facet[bridge.GroupView]{
		{Param: "state", Title: s.t("State"), Value: func(g bridge.GroupView) string { return g.Status }, Order: []string{"firing", "resolved"},
			Label: func(v string) string {
				if v == "firing" {
					return s.t("Firing")
				}
				return s.t("Resolved")
			}},
		{Param: "severity", Title: s.t("Severity"), Value: func(g bridge.GroupView) string { return g.Severity }, Order: config.Severities, Label: s.severity},
		{Param: "service", Title: s.t("Service"), Value: func(g bridge.GroupView) string { return g.Service }},
		{Param: "acknowledged", Title: s.t("Acknowledged"), Value: func(g bridge.GroupView) string { return s.yesNo(g.Ack != nil) }, Order: []string{"yes", "no"}, Label: s.yesNoLabel},
		{Param: "silenced", Title: s.t("Silenced"), Value: func(g bridge.GroupView) string { return s.yesNo(silenced(g)) }, Order: []string{"yes", "no"}, Label: s.yesNoLabel},
	}
	match := func(g bridge.GroupView, q string) bool {
		texts := []string{g.Title, g.Service, g.Description, g.ID}
		for k, v := range g.Labels {
			texts = append(texts, k+"="+v)
		}
		return contains(q, texts...)
	}
	kept, l := filter(s, consolePath("/alerts"), r.URL.Query(), groups, facets, match)
	l.SearchHint = s.t("Title, service or label")
	p := listPage[alertRow]{shell: s.shellFor(r, v, "alerts", s.t("Alerts")), L: s.listLabels(), List: l}
	p.Count = l.Total
	for _, g := range kept {
		row := alertRow{Title: g.Title, Href: link.AlertPath(g.ID), Severity: s.severity(g.Severity), SeverityTone: tone(g.Severity),
			Service: g.Service, Since: s.clock(g.StartsAt), Acked: g.Ack != nil, Silenced: silenced(g)}
		if g.Status == "firing" {
			row.State, row.StateTone, row.When = s.t("Firing"), "danger", s.dur(now.Sub(g.StartsAt))
		} else {
			row.State, row.StateTone = s.t("Resolved"), "success"
			if g.ResolvedAt != nil {
				row.When = s.dur(g.ResolvedAt.Sub(g.StartsAt))
			}
		}
		p.Rows = append(p.Rows, row)
	}
	s.render(w, alertsTmpl, p)
}

// ---- sessions

// maxScan is how many of the newest sessions a list walks; the list says when there are more.
const maxScan = 10000

type sessionItem struct {
	ID, Href, Agent, Via, Machine, MachineHref, Kind, Command, Started, Duration, Exit string
	Failed, Open                                                                       bool
}

func (s *Server) sessionItem(r store.SessionRecord) sessionItem {
	it := sessionItem{ID: r.ID, Href: link.SessionPath(r.ID), Agent: r.Agent, Via: s.via(r.Via), Machine: r.Machine,
		MachineHref: consolePath("/machines/") + url.PathEscape(r.Machine), Kind: s.kind(r.Kind), Command: shorten(r.Command, 160),
		Started: s.clock(r.Started), Exit: s.exit(r.Exit), Failed: r.Exit != nil && *r.Exit != 0, Open: r.Ended.IsZero()}
	if !r.Ended.IsZero() {
		it.Duration = s.dur(r.Ended.Sub(r.Started))
	}
	return it
}

// shorten cuts a text to n runes, marking the cut.
func shorten(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n]) + "…"
}

func (s *Server) exitClass(r store.SessionRecord) string {
	switch {
	case r.Ended.IsZero():
		return "open"
	case r.Exit == nil:
		return "none"
	case *r.Exit == 0:
		return "ok"
	}
	return "failed"
}

var sessionsTmpl = parse("sessions.html")

type sessionsPage struct {
	listPage[sessionItem]
	More string
}

// sessionTable is a table of sessions, as the sessions' and a machine's pages show them.
type sessionTable struct {
	L    listLabels
	Rows []sessionItem
}

func (p sessionsPage) Table() sessionTable { return sessionTable{L: p.L, Rows: p.Rows} }

func (s *Server) sessionsList(w http.ResponseWriter, r *http.Request, v *viewer) {
	var recs []store.SessionRecord
	more := false
	s.Store.EachSession(func(rec store.SessionRecord) bool {
		if len(recs) == maxScan {
			more = true
			return false
		}
		recs = append(recs, rec)
		return true
	})
	exits := map[string]string{"ok": s.t("Succeeded"), "failed": s.t("Failed"), "open": s.t("Still open"), "none": s.t("No exit status")}
	facets := []facet[store.SessionRecord]{
		{Param: "agent", Title: s.t("Agent"), Value: func(r store.SessionRecord) string { return r.Agent }},
		{Param: "machine", Title: s.t("Machine"), Value: func(r store.SessionRecord) string { return r.Machine }},
		{Param: "kind", Title: s.t("Kind"), Value: func(r store.SessionRecord) string { return r.Kind }, Label: s.kind},
		{Param: "via", Title: s.t("Via"), Value: func(r store.SessionRecord) string { return r.Via }, Label: s.via},
		{Param: "exit", Title: s.t("Exit status"), Value: s.exitClass, Order: []string{"ok", "failed", "open", "none"}, Label: func(v string) string { return exits[v] }},
		{Param: "day", Title: s.t("Day"), Value: func(r store.SessionRecord) string { return s.day(r.Started) }},
	}
	// days read newest first, not by how busy they were
	facets[5].Order = days(recs, s.day)
	match := func(r store.SessionRecord, q string) bool { return contains(q, r.ID, r.Agent, r.Machine, r.Command) }
	kept, l := filter(s, consolePath("/sessions"), r.URL.Query(), recs, facets, match)
	l.SearchHint = s.t("Command, agent or machine")
	p := sessionsPage{listPage: listPage[sessionItem]{shell: s.shellFor(r, v, "sessions", s.t("Sessions")), L: s.listLabels(), List: l}}
	p.Count = l.Total
	if more {
		p.More = s.t("Only the newest %s sessions are listed here; the API lists the rest.", num(maxScan))
	}
	for _, rec := range kept {
		p.Rows = append(p.Rows, s.sessionItem(rec))
	}
	s.render(w, sessionsTmpl, p)
}

// days lists the days of the sessions, the newest first.
func days(recs []store.SessionRecord, day func(time.Time) string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range recs {
		d := day(r.Started)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// ---- agents

type hookRow struct {
	Name, Kind, Where, Severities, Service string
	Resolved                               bool
	Calls                                  string
}

type callRow struct {
	Hook, Event, Status, StatusTone, Code, Attempts, When, Alert, AlertHref, Error string
}

type agentsPage struct {
	listPage[callRow]
	Hooks []hookRow
}

var agentsTmpl = parse("agents.html")

func (s *Server) agents(w http.ResponseWriter, r *http.Request, v *viewer) {
	all := s.calls()
	byHook := map[string]int{}
	for _, c := range all {
		byHook[c.Hook]++
	}
	events := func(e string) string {
		if e == "resolved" {
			return s.t("Resolved")
		}
		return s.t("Firing")
	}
	facets := []facet[call]{
		{Param: "hook", Title: s.t("Hook"), Value: func(c call) string { return c.Hook }},
		{Param: "event", Title: s.t("Event"), Value: func(c call) string { return c.Event }, Order: []string{"firing", "resolved"}, Label: events},
		{Param: "status", Title: s.t("Status"), Value: func(c call) string { return c.Status }, Order: []string{"ok", "failed", "pending"}, Label: s.callStatus},
	}
	match := func(c call, q string) bool { return contains(q, c.Hook, c.title, c.alert, c.Error) }
	kept, l := filter(s, consolePath("/agents"), r.URL.Query(), all, facets, match)
	l.SearchHint = s.t("Hook or alert")
	p := agentsPage{listPage: listPage[callRow]{shell: s.shellFor(r, v, "agents", s.t("Agents")), L: s.listLabels(), List: l}}
	p.Count = l.Total
	for _, h := range s.Hooks {
		row := hookRow{Name: h.Name, Kind: h.Kind, Severities: strings.Join(h.Severities, ", "), Service: h.Service, Resolved: h.Resolved, Calls: num(byHook[h.Name])}
		switch h.Kind {
		case "github":
			row.Where = h.Repository + " " + h.Workflow
		default:
			if u, err := url.Parse(h.URL); err == nil {
				row.Where = u.Host // never the path or the query, which may carry a secret
			}
		}
		p.Hooks = append(p.Hooks, row)
	}
	for _, c := range kept {
		row := callRow{Hook: c.Hook, Event: events(c.Event), Status: s.callStatus(c.Status), StatusTone: tone(c.Status), Attempts: strconv.Itoa(c.Attempts),
			When: s.clock(c.At), Alert: c.title, AlertHref: link.AlertPath(c.alert), Error: c.Error}
		if c.Code > 0 {
			row.Code = strconv.Itoa(c.Code)
		}
		p.Rows = append(p.Rows, row)
	}
	s.render(w, agentsTmpl, p)
}

// ---- the audit log

type auditRow struct{ Time, Actor, Via, Action, Target, Detail string }

var auditTmpl = parse("audit.html")

func (s *Server) audit(w http.ResponseWriter, r *http.Request, v *viewer) {
	entries := s.Store.AuditLog(10000)
	facets := []facet[store.AuditEntry]{
		{Param: "actor", Title: s.t("Who"), Value: func(e store.AuditEntry) string { return e.Actor }},
		{Param: "action", Title: s.t("Action"), Value: func(e store.AuditEntry) string { return e.Action }},
		{Param: "via", Title: s.t("Via"), Value: func(e store.AuditEntry) string { return e.Via }, Label: s.via},
	}
	match := func(e store.AuditEntry, q string) bool { return contains(q, e.Actor, e.Action, e.Target, e.Detail) }
	kept, l := filter(s, consolePath("/audit"), r.URL.Query(), entries, facets, match)
	l.SearchHint = s.t("Who, what or detail")
	p := listPage[auditRow]{shell: s.shellFor(r, v, "audit", s.t("Audit log")), L: s.listLabels(), List: l}
	p.Count = l.Total
	for _, e := range kept {
		p.Rows = append(p.Rows, auditRow{Time: s.clock(e.Time), Actor: e.Actor, Via: s.via(e.Via), Action: e.Action, Target: e.Target, Detail: e.Detail})
	}
	s.render(w, auditTmpl, p)
}

// ---- change events

type changeItem struct{ Time, Source, Kind, Service, Machine, Summary, Href, PostedBy string }

var changesTmpl = parse("changes.html")

func (s *Server) changes(w http.ResponseWriter, r *http.Request, v *viewer) {
	all := s.Store.Changes(store.ChangeFilter{Limit: maxScan})
	facets := []facet[store.Change]{
		{Param: "source", Title: s.t("Source"), Value: func(c store.Change) string { return c.Source }},
		{Param: "kind", Title: s.t("Kind"), Value: func(c store.Change) string { return c.Kind }},
		{Param: "service", Title: s.t("Service"), Value: func(c store.Change) string { return c.Service }},
		{Param: "machine", Title: s.t("Machine"), Value: func(c store.Change) string { return c.Machine }},
	}
	match := func(c store.Change, q string) bool {
		return contains(q, c.Summary, c.Source, c.Kind, c.Service, c.Machine, c.Revision, c.Object)
	}
	kept, l := filter(s, consolePath("/changes"), r.URL.Query(), all, facets, match)
	l.SearchHint = s.t("Summary, revision or object")
	p := listPage[changeItem]{shell: s.shellFor(r, v, "changes", s.t("Changes")), L: s.listLabels(), List: l}
	p.Count = l.Total
	for _, c := range kept {
		p.Rows = append(p.Rows, changeItem{Time: s.clock(c.Time), Source: c.Source, Kind: c.Kind, Service: c.Service, Machine: c.Machine,
			Summary: c.Summary, Href: external(c.URL), PostedBy: c.PostedBy})
	}
	s.render(w, changesTmpl, p)
}
