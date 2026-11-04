package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The overview: is everything all right? The figures that answer it, each linking to its list.

// reportingWindow is how recently a probe location must have written to count as reporting.
const reportingWindow = 5 * time.Minute

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

type severityRow struct {
	Label, Tone, Count, Href string
}

type alertItem struct {
	Title, Href, Severity, SeverityTone, Since, Service string
	Acked, Silenced                                     bool
}

type callItem struct {
	Hook, Alert, AlertHref, Status, StatusTone, At string
}

type overviewLabels struct {
	Firing, AllAlerts, Woken, AllCalls, Beacon, ConfigVersion, Version, Channel, NoneFiring, NoneWoken string
	Acked, Silenced                                                                                    string
}

type overviewPage struct {
	shell
	L          overviewLabels
	Stats      []stat
	Severities []severityRow
	Firing     []alertItem
	Calls      []callItem
	Channel    stat
	Facts      []kvLink
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

func (s *Server) overview(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := s.figures(v)
	p := overviewPage{shell: s.shellFor(r, v, "overview", s.t("Overview")), L: overviewLabels{
		Firing: s.t("Firing alerts"), AllAlerts: s.t("All alerts"), Woken: s.t("Agents woken today"), AllCalls: s.t("All agent calls"),
		Beacon: s.t("Beacon"), ConfigVersion: s.t("Configuration version"), Version: s.t("Beacon version"), Channel: s.t("The SSH channel"),
		NoneFiring: s.t("Nothing is firing."), NoneWoken: s.t("No agent was woken today."), Acked: s.t("Acknowledged"),
		Silenced: s.t("Silenced"),
	}}
	p.Crumbs = []crumb{{Label: s.t("Console")}}
	firingTone := ""
	if f.Firing["critical"] > 0 {
		firingTone = "danger"
	} else if f.FiringTotal > 0 {
		firingTone = "warning"
	}
	p.Stats = append(p.Stats, stat{Label: s.t("Firing alerts"), Value: num(f.FiringTotal), Icon: "triangle-alert", Tone: firingTone,
		Meta: s.t("%d critical, %d warning, %d info", f.Firing["critical"], f.Firing["warning"], f.Firing["info"]), Href: consolePath("/alerts?state=firing")})
	loc := stat{Label: s.t("Probe locations"), Icon: "activity", Href: consolePath("/config#locations")}
	if f.ReportingKnown {
		loc.Value = s.t("%d of %d", f.Reporting, f.Expected)
		loc.Meta = s.t("reporting in the last 5 minutes")
		if f.Reporting < f.Expected {
			loc.Tone = "warning"
		}
	} else {
		loc.Value, loc.Meta = num(f.Expected), s.t("expected; the gateway is off here")
	}
	p.Stats = append(p.Stats, loc)
	mach := stat{Label: s.t("Machines"), Value: num(f.Machines), Icon: "server", Href: consolePath("/machines"),
		Meta: s.t("%d critical, %d of %d behind NAT connected", f.Critical, f.Connected, f.Connectors)}
	if f.Connected < f.Connectors {
		mach.Tone = "warning"
	}
	p.Stats = append(p.Stats, mach)
	woken := stat{Label: s.t("Agents woken today"), Value: num(f.Woken), Icon: "bot", Href: consolePath("/hooks"),
		Meta: s.t("%d woken, %d failed, %d waiting", f.WokenOK, f.WokenFailed, f.Woken-f.WokenOK-f.WokenFailed)}
	if f.WokenFailed > 0 {
		woken.Tone = "danger"
	}
	p.Stats = append(p.Stats, woken)
	p.Stats = append(p.Stats, stat{Label: s.t("Sessions today"), Value: num(f.SessionsToday), Icon: "terminal",
		Href: consolePath("/sessions?day=") + url.QueryEscape(s.day(s.now())), Meta: s.t("since %s", s.today().Format("15:04"))})

	switch {
	case !f.ChannelOn:
		p.Channel = stat{Label: s.t("The SSH channel"), Value: s.t("Off"), Meta: s.t("ssh.listen is not set"), Href: consolePath("/machines")}
	case !f.Paused.IsZero():
		p.Channel = stat{Label: s.t("The SSH channel"), Value: s.t("Paused"), Tone: "danger", Href: consolePath("/machines"),
			Meta: s.t("since %s (%s)", s.clock(f.Paused), s.dur(s.now().Sub(f.Paused)))}
	default:
		p.Channel = stat{Label: s.t("The SSH channel"), Value: s.t("Running"), Tone: "success", Href: consolePath("/machines"), Meta: s.t("Agents reach machines")}
	}
	for _, sev := range config.Severities {
		p.Severities = append(p.Severities, severityRow{Label: s.severity(sev), Tone: tone(sev), Count: num(f.Firing[sev]),
			Href: consolePath("/alerts?severity=") + sev + "&state=firing"})
	}
	for _, g := range s.Bridge.Groups(true) {
		if len(p.Firing) == 10 {
			break
		}
		p.Firing = append(p.Firing, alertItem{Title: g.Title, Href: link.AlertPath(g.ID), Severity: s.severity(g.Severity), SeverityTone: tone(g.Severity),
			Since: s.ago(g.StartsAt), Service: g.Service, Acked: g.Ack != nil, Silenced: g.Silence != nil && s.now().Before(g.Silence.Until)})
	}
	for _, c := range s.calls() {
		if c.At.Before(s.today()) || len(p.Calls) == 8 {
			break
		}
		p.Calls = append(p.Calls, c.item)
	}
	cv := kvLink{K: s.t("Configuration version"), V: strconv.FormatInt(f.ConfigVersion, 10), Href: consolePath("/config")}
	if f.ConfigBy != "" {
		cv.V = s.t("%d, by %s via %s, %s", f.ConfigVersion, f.ConfigBy, f.ConfigVia, f.ConfigAt)
	}
	p.Facts = []kvLink{cv, {K: s.t("Beacon version"), V: s.version()}, {K: s.t("Signed in as"), V: v.name(), Href: consolePath("/account")}}
	s.render(w, overviewTmpl, p)
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
