package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The overview: is everything all right? A sentence that answers it; the figures behind it, each
// linking to its list; what needs attention, the most urgent first: the alerts firing, and what Beacon
// sees going wrong that no alert names yet (services failing, probe locations gone quiet, machines
// behind NAT not connected, agents a hook failed to wake, the channel paused); then what agents did
// and what changed lately, the status pages as the public sees them, and what is coming up.

// reportingWindow is how recently a probe location must have written to count as reporting.
const reportingWindow = 5 * time.Minute

// expiringWithin is how soon a resource's expiry shows on the overview, as the rules warn of it.
const expiringWithin = 30 * 24 * time.Hour

// ReportingFrom reads when each probe location or host last wrote through the gateway from the metric
// the gateway keeps (beacon_gateway_last_write_timestamp_seconds), for Server.Reporting.
func ReportingFrom(m *metrics.Metrics) func() map[string]time.Time {
	return func() map[string]time.Time {
		out := map[string]time.Time{}
		for _, s := range m.Samples("beacon_gateway_last_write_timestamp_seconds") {
			if who := metrics.LabelValue(s.Labels, "subject"); who != "" {
				out[who] = time.Unix(int64(s.Value), 0)
			}
		}
		return out
	}
}

type stat struct {
	Label, Value, Meta, Href, Icon, Tone string
}

type callItem struct {
	Hook, Alert, AlertHref, Status, StatusTone, At string
}

// health is the overview's answer, in a sentence.
type health struct{ Title, Text, Tone, Icon string }

// attentionItem is one thing that needs someone: an alert, or what Beacon sees that no alert names.
type attentionItem struct {
	Icon, Tone, Title, Href, Sub, Badge, BadgeTone, When string
	Acked, Silenced                                      bool
	kind                                                 string // alert, service, channel, location, machine or hook
}

// feedItem is a line of a short list beside the rest: a session, a change, a page, a coming event.
type feedItem struct {
	Icon, Title, Href, Sub, SubHref, Badge, Tone, When string
	Mono                                               bool // the sub line is a command
	at                                                 time.Time
}

type overviewLabels struct {
	Attention, AllAlerts, Calm, CalmText, Sessions, AllSessions, NoSessions, Changes, AllChanges, NoChanges string
	Pages, AllPages, NoPages, Coming, NoComing, Woken, AllCalls, NoneWoken, Beacon, Acked, Silenced         string
}

type overviewPage struct {
	shell
	L         overviewLabels
	Health    health
	Stats     []stat
	Attention []attentionItem
	Sessions  []feedItem
	Changes   []feedItem
	Pages     []feedItem
	Coming    []feedItem
	Calls     []callItem
	Channel   stat
	Facts     []kvLink
}

type kvLink struct{ K, V, Href string }

// overviewFigures are the overview's numbers, apart from how they are shown (the test compares them
// with the store).
type overviewFigures struct {
	Firing                        map[string]int
	FiringTotal                   int
	Expected, Reporting           int
	ReportingKnown                bool
	Machines, Critical            int
	Connectors, Connected         int
	Woken, WokenOK, WokenFailed   int
	SessionsToday                 int
	Paused                        time.Time
	ChannelOn                     bool
	ConfigVersion                 int64
	ConfigBy, ConfigVia, ConfigAt string
}

func (s *Server) figures(v *viewer) overviewFigures {
	now, today := s.now(), s.today()
	f := overviewFigures{Firing: map[string]int{}}
	for _, g := range s.Store.Groups() {
		if g.Firing() {
			f.Firing[g.Severity]++
			f.FiringTotal++
		}
		for _, c := range g.Agents {
			if c.Event == "firing" && !c.At.Before(today) {
				f.Woken++
				switch c.Status {
				case "ok":
					f.WokenOK++
				case "failed":
					f.WokenFailed++
				}
			}
		}
	}
	if s.Config != nil {
		cat := s.Config.Catalog()
		var seen map[string]time.Time
		if s.Reporting != nil {
			seen, f.ReportingKnown = s.Reporting(), true
		}
		for _, l := range cat.Locations {
			if !cat.Expected(l, now) {
				continue
			}
			f.Expected++
			if t, ok := seen[l.ID]; ok && now.Sub(t) < reportingWindow {
				f.Reporting++
			}
		}
		f.ConfigVersion = s.Config.Version()
		if h, err := s.Config.History(configHistoryFilter(1)); err == nil && len(h) == 1 {
			f.ConfigBy, f.ConfigVia, f.ConfigAt = h[0].Actor, s.via(h[0].Via), s.ago(h[0].Time)
		}
	}
	if s.SSH != nil {
		f.ChannelOn, f.Paused = true, s.SSH.PausedSince()
		if l, err := s.SSH.List(v.gate(), ""); err == nil {
			for _, m := range l.Machines {
				f.Machines++
				if m.Critical {
					f.Critical++
				}
				if m.Connector {
					f.Connectors++
					if m.Connected != nil && *m.Connected {
						f.Connected++
					}
				}
			}
		}
	} else {
		f.Machines = len(s.Store.Machines())
	}
	s.Store.EachSession(func(r store.SessionRecord) bool {
		if r.Started.Before(today) {
			return false
		}
		f.SessionsToday++
		return true
	})
	return f
}

// serviceHealth is how the services the probes check are doing: how many there are, how many answer
// at every location, those in trouble (with the state, the tone and the first reason), and why the
// probes could not be read, if they could not.
type serviceHealth struct {
	Total, OK int
	Trouble   []attentionItem
	Problem   string
}

func (s *Server) serviceHealth(ctx context.Context) serviceHealth {
	sc := s.serviceConfig()
	h := serviceHealth{Total: len(sc.names)}
	if h.Total == 0 {
		return h
	}
	now := s.now()
	pe := s.Bridge.Probes(ctx, sc.names, bridge.Window{From: now.Add(-monitorWindow), To: now}, maxMonitorResults)
	h.Problem = pe.Error
	only := ""
	if len(sc.names) == 1 {
		only = sc.names[0]
	}
	states := probeStates(pe, only)
	for _, svc := range sc.names {
		ps := states[svc]
		state, tn := s.state(ps)
		switch tn {
		case "success":
			h.OK++
		case "danger", "warning":
			it := attentionItem{Icon: "activity", Tone: tn, Title: svc, Href: consolePath("/monitors/") + url.PathEscape(svc), Sub: state,
				Badge: s.t("Service"), BadgeTone: tn, kind: "service"}
			if ps.reason != "" {
				it.Sub = state + ": " + ps.reason
			}
			if ps.since != nil {
				it.When = s.ago(*ps.since)
			}
			h.Trouble = append(h.Trouble, it)
		}
	}
	return h
}

// attention is what needs someone now, the most urgent first: the alerts firing, then what Beacon sees
// going wrong that no firing alert names.
func (s *Server) attention(v *viewer, f overviewFigures, sh serviceHealth) []attentionItem {
	var out []attentionItem
	alerting := map[string]bool{}
	for _, g := range s.Bridge.Groups(true) {
		alerting[g.Service] = true
		silenced := g.Silence != nil && s.now().Before(g.Silence.Until)
		out = append(out, attentionItem{Icon: "triangle-alert", Tone: tone(g.Severity), Title: g.Title, Href: link.AlertPath(g.ID), Sub: g.Service,
			Badge: s.severity(g.Severity), BadgeTone: tone(g.Severity), When: s.ago(g.StartsAt), Acked: g.Ack != nil, Silenced: silenced, kind: "alert"})
	}
	for _, it := range sh.Trouble {
		if !alerting[it.Title] {
			out = append(out, it)
		}
	}
	if !f.Paused.IsZero() {
		out = append(out, attentionItem{Icon: "power", Tone: "danger", Title: s.t("Agents' access to machines is paused"), Href: consolePath("/machines"),
			Sub: s.t("since %s (%s)", s.clock(f.Paused), s.dur(s.now().Sub(f.Paused))), Badge: s.t("The SSH channel"), BadgeTone: "danger", kind: "channel"})
	}
	if s.Config != nil && f.ReportingKnown {
		cat, now, seen := s.Config.Catalog(), s.now(), s.Reporting()
		for _, l := range cat.Locations {
			t, ok := seen[l.ID]
			if !cat.Expected(l, now) || (ok && now.Sub(t) < reportingWindow) {
				continue
			}
			name := l.Name
			if name == "" {
				name = l.ID
			}
			it := attentionItem{Icon: "signal", Tone: "warning", Title: name, Href: consolePath("/config#locations"),
				Sub: s.t("No probe results in the last 5 minutes"), Badge: s.t("Probe location"), BadgeTone: "warning", kind: "location"}
			if ok {
				it.When = s.ago(t)
			}
			out = append(out, it)
		}
	}
	if s.SSH != nil {
		if l, err := s.SSH.List(v.gate(), ""); err == nil {
			for _, m := range l.Machines {
				if !m.Connector || (m.Connected != nil && *m.Connected) {
					continue
				}
				tn := "warning"
				if m.Critical {
					tn = "danger"
				}
				out = append(out, attentionItem{Icon: "server", Tone: tn, Title: m.Name, Href: consolePath("/machines/") + url.PathEscape(m.Name),
					Sub: s.t("Behind NAT, not connected"), Badge: s.t("Machine"), BadgeTone: tn, kind: "machine"})
			}
		}
	}
	for _, c := range s.calls() {
		if c.At.Before(s.today()) {
			break
		}
		if c.Status == "failed" {
			out = append(out, attentionItem{Icon: "bot", Tone: "danger", Title: s.t("Hook %s failed to wake an agent", c.Hook), Href: c.item.AlertHref,
				Sub: c.title, Badge: s.t("Agent call"), BadgeTone: "danger", When: s.ago(c.At), kind: "hook"})
		}
	}
	// the worst news first; an alert someone took on, or silenced, after those nobody has
	rank := func(a attentionItem) int {
		r := 2 * toneRank(a.Tone)
		if !a.Acked && !a.Silenced {
			r++
		}
		return r
	}
	slices.SortStableFunc(out, func(a, b attentionItem) int { return rank(b) - rank(a) })
	return out
}

// healthOf is the overview's answer: everything is running, or how many things need attention, and
// what they are.
func (s *Server) healthOf(items []attentionItem, sh serviceHealth) health {
	if len(items) == 0 {
		h := health{Tone: "success", Icon: "circle-check", Title: s.t("Everything is running")}
		switch {
		case sh.Problem != "":
			h.Tone, h.Icon, h.Text = "warning", "circle-alert", s.t("Nothing is firing, but the probes could not be read: %s", sh.Problem)
		case sh.Total > 0:
			h.Text = s.t("Nothing is firing, and all %d services answer at every location.", sh.Total)
		default:
			h.Text = s.t("Nothing is firing.")
		}
		return h
	}
	h := health{Tone: "warning", Icon: "triangle-alert", Title: s.t("%d things need attention", len(items))}
	if len(items) == 1 {
		h.Title = s.t("One thing needs attention")
	}
	kinds := map[string]int{}
	for _, it := range items {
		if it.Tone == "danger" {
			h.Tone = "danger"
		}
		kinds[it.kind]++
	}
	var parts []string
	for _, k := range []struct{ kind, name string }{
		{"alert", s.t("Firing alerts")}, {"service", s.t("Services in trouble")}, {"channel", s.t("The SSH channel")},
		{"location", s.t("Probe locations not reporting")}, {"machine", s.t("Machines not connected")}, {"hook", s.t("Agents not woken")},
	} {
		switch n := kinds[k.kind]; {
		case n == 0:
		case k.kind == "channel":
			parts = append(parts, s.t("%s: %s", k.name, s.t("Paused")))
		default:
			parts = append(parts, s.t("%s: %d", k.name, n))
		}
	}
	h.Text = strings.Join(parts, s.t("; ")) + s.t(".")
	return h
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := s.figures(v)
	sh := s.serviceHealth(r.Context())
	p := overviewPage{shell: s.shellFor(r, v, "overview", s.t("Overview")), L: overviewLabels{
		Attention: s.t("Needs attention"), AllAlerts: s.t("All alerts"), Calm: s.t("Nothing needs attention"),
		CalmText: s.t("No alert is firing, and the services, probe locations, machines and hooks are as they should be."),
		Sessions: s.t("Latest sessions"), AllSessions: s.t("All sessions"), NoSessions: s.t("No agent has worked on a machine yet."),
		Changes: s.t("Latest changes"), AllChanges: s.t("All changes"), NoChanges: s.t("No deployment or other change was reported yet."),
		Pages: s.t("Status pages"), AllPages: s.t("All status pages"), NoPages: s.t("No status pages."),
		Coming: s.t("Coming up"), NoComing: s.t("No maintenance is planned, and nothing expires in the next 30 days."),
		Woken: s.t("Agents woken today"), AllCalls: s.t("All agent calls"), NoneWoken: s.t("No agent was woken today."),
		Beacon: s.t("Beacon"), Acked: s.t("Acknowledged"), Silenced: s.t("Silenced"),
	}}
	p.Crumbs = []crumb{{Label: s.t("Console")}}
	p.Attention = s.attention(v, f, sh)
	p.Health = s.healthOf(p.Attention, sh)
	p.Stats = s.overviewStats(f, sh)

	switch {
	case !f.ChannelOn:
		p.Channel = stat{Label: s.t("The SSH channel"), Value: s.t("Off"), Meta: s.t("ssh.listen is not set"), Href: consolePath("/machines")}
	case !f.Paused.IsZero():
		p.Channel = stat{Label: s.t("The SSH channel"), Value: s.t("Paused"), Tone: "danger", Href: consolePath("/machines"),
			Meta: s.t("since %s (%s)", s.clock(f.Paused), s.dur(s.now().Sub(f.Paused)))}
	default:
		p.Channel = stat{Label: s.t("The SSH channel"), Value: s.t("Running"), Tone: "success", Href: consolePath("/machines"), Meta: s.t("Agents reach machines")}
	}
	s.Store.EachSession(func(rec store.SessionRecord) bool {
		it := s.sessionItem(rec)
		fi := feedItem{Icon: "terminal", Title: s.t("%s on %s", rec.Agent, rec.Machine), Href: it.Href, Sub: it.Command, Mono: it.Command != "",
			When: s.ago(rec.Started)}
		if fi.Sub == "" {
			fi.Sub = it.Kind
		}
		switch {
		case it.Open:
			fi.Badge, fi.Tone = s.t("Still open"), "info"
		case it.Exit != "":
			fi.Badge, fi.Tone = it.Exit, "success"
			if it.Failed {
				fi.Tone = "danger"
			}
		}
		p.Sessions = append(p.Sessions, fi)
		return len(p.Sessions) < 6
	})
	for _, c := range s.Store.Changes(store.ChangeFilter{Limit: 5}) {
		about := strings.TrimSpace(c.Service + " " + c.Machine)
		p.Changes = append(p.Changes, feedItem{Icon: "history", Title: c.Summary, Sub: about, Badge: c.Source, When: s.ago(c.Time)})
	}
	p.Pages = s.pageFeed(r)
	p.Coming = s.coming()
	for _, c := range s.calls() {
		if c.At.Before(s.today()) || len(p.Calls) == 6 {
			break
		}
		p.Calls = append(p.Calls, c.item)
	}
	cv := kvLink{K: s.t("Configuration version"), V: strconv.FormatInt(f.ConfigVersion, 10), Href: consolePath("/config")}
	if f.ConfigBy != "" {
		cv.V = s.t("%d, by %s via %s, %s", f.ConfigVersion, f.ConfigBy, f.ConfigVia, f.ConfigAt)
	}
	p.Facts = []kvLink{cv, {K: s.t("Beacon version"), V: s.version()}}
	s.render(w, overviewTmpl, p)
}

// overviewStats are the figures that answer "is everything all right?", each linking to its list.
func (s *Server) overviewStats(f overviewFigures, sh serviceHealth) []stat {
	firing := stat{Label: s.t("Firing alerts"), Value: num(f.FiringTotal), Icon: "triangle-alert", Href: consolePath("/alerts?state=firing"),
		Meta: s.t("%d critical, %d warning, %d info", f.Firing["critical"], f.Firing["warning"], f.Firing["info"])}
	if f.Firing["critical"] > 0 {
		firing.Tone = "danger"
	} else if f.FiringTotal > 0 {
		firing.Tone = "warning"
	}
	svc := stat{Label: s.t("Services"), Icon: "activity", Href: consolePath("/monitors")}
	switch {
	case sh.Total == 0:
		svc.Value, svc.Meta = "0", s.t("none probed yet")
	case sh.Problem != "":
		svc.Value, svc.Meta, svc.Tone = num(sh.Total), s.t("the probes could not be read"), "warning"
	default:
		svc.Value, svc.Meta = s.t("%d of %d", sh.OK, sh.Total), s.t("answer at every location")
		for _, t := range sh.Trouble {
			if svc.Tone != "danger" {
				svc.Tone = t.Tone
			}
		}
	}
	loc := stat{Label: s.t("Probe locations"), Icon: "signal", Href: consolePath("/config#locations")}
	if f.ReportingKnown {
		loc.Value = s.t("%d of %d", f.Reporting, f.Expected)
		loc.Meta = s.t("reporting in the last 5 minutes")
		if f.Reporting < f.Expected {
			loc.Tone = "warning"
		}
	} else {
		loc.Value, loc.Meta = num(f.Expected), s.t("expected; the gateway is off here")
	}
	mach := stat{Label: s.t("Machines"), Value: num(f.Machines), Icon: "server", Href: consolePath("/machines"),
		Meta: s.t("%d critical, %d of %d behind NAT connected", f.Critical, f.Connected, f.Connectors)}
	if f.Connected < f.Connectors {
		mach.Tone = "warning"
	}
	work := stat{Label: s.t("Agent sessions today"), Value: num(f.SessionsToday), Icon: "bot",
		Href: consolePath("/sessions?day=") + url.QueryEscape(s.day(s.now())),
		Meta: s.t("%d woken by alerts, %d failed, %d waiting", f.Woken, f.WokenFailed, f.Woken-f.WokenOK-f.WokenFailed)}
	if f.WokenFailed > 0 {
		work.Tone = "danger"
	}
	return []stat{firing, svc, loc, mach, work}
}

// pageFeed is each status page as the public sees it now.
func (s *Server) pageFeed(r *http.Request) []feedItem {
	var out []feedItem
	for _, def := range s.Status.Pages() {
		it := feedItem{Icon: "globe", Title: s.pageTitle(def), Href: consolePath("/status-pages/") + url.PathEscape(def.Name)}
		pg, err := s.Status.Page(r.Context(), def.Name, string(s.Lang))
		if err != nil {
			it.Badge, it.Sub = s.t("No data"), s.t("The page could not be computed")
		} else {
			it.Badge, it.Tone = s.pageState(pg.Status)
			it.Sub = pg.Summary
		}
		out = append(out, it)
	}
	return out
}

// coming is what is planned or due: the maintenance windows in progress or to come, and the resources
// that expire within 30 days (or have), the soonest first.
func (s *Server) coming() []feedItem {
	now := s.now()
	var out []feedItem
	for _, m := range s.Store.MaintenanceWindows() {
		state, tn, over := s.windowState(m, now)
		if over {
			continue
		}
		it := feedItem{Icon: "wrench", Title: m.Title, Href: consolePath("/silences#maintenance"), Sub: strings.Join(m.Services, ", "),
			Badge: state, Tone: tn, When: s.t("from %s", s.short(m.StartsAt)), at: m.StartsAt}
		if !m.StartsAt.After(now) {
			it.When, it.at = s.t("until %s", s.short(m.End())), now
		}
		out = append(out, it)
	}
	if s.Config != nil {
		for _, x := range s.Config.Catalog().Resources {
			at := x.ExpiresAt()
			if at.IsZero() || at.Sub(now) > expiringWithin {
				continue
			}
			it := feedItem{Icon: "calendar", Title: x.Name, Href: consolePath("/monitors#resources"), Sub: x.Kind, When: x.Expires, at: at,
				Badge: s.t("in %d days", int(at.Sub(now).Hours()/24)), Tone: "warning"}
			switch {
			case !at.After(now):
				it.Badge, it.Tone = s.t("Expired"), "danger"
			case at.Sub(now) < 7*24*time.Hour:
				it.Tone = "danger"
			}
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b feedItem) int { return a.at.Compare(b.at) })
	return out
}

var overviewTmpl = parse("overview.html")

// call is an agent call with its alert, for the overview and the agents' page.
type call struct {
	store.AgentCall
	item         callItem
	alert, title string
}

// calls are every agent call of the groups Beacon holds, the newest first.
func (s *Server) calls() []call {
	var out []call
	for _, g := range s.Bridge.Groups(false) {
		for _, c := range g.Agents {
			x := call{AgentCall: c, alert: g.ID, title: g.Title}
			x.item = callItem{Hook: c.Hook, Alert: g.Title, AlertHref: link.AlertPath(g.ID), Status: s.callStatus(c.Status), StatusTone: tone(c.Status), At: s.clock(c.At)}
			out = append(out, x)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (s *Server) callStatus(status string) string {
	switch status {
	case "ok":
		return s.t("Done")
	case "failed":
		return s.t("Failed")
	}
	return s.t("Pending")
}
