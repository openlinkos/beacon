package web

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/web/link"
)

// What Beacon monitors and how it is doing now: every service the probes check, with what
// the locations see of it; the heartbeats with their last success; the resources with when they
// expire. A service's page lists its targets, what each location saw of them in the last half hour,
// the status pages that show it and its alerts. The configuration page shows the configuration and
// its history, including the HTTP monitors added from the console.

// monitorWindow is how far back a service's probes are read.
const monitorWindow = 30 * time.Minute

// maxMonitorResults bounds the probe results read for the list: every target at every location.
const maxMonitorResults = 5000

type monitorLabels struct {
	Services, Heartbeats, Resources, Service, State, Targets, Pages, Severity, Checked, Machine, MaxAge string
	LastSuccess, Expires, Kind, Note, Description, None, NoServices, Target, Module, Set, Domain        string
	Visibility, Alerts, Started, Window, OnPages, Name, Add, Schedule                                   string
	RefreshResults, MetricsDetails, WaitingHint, NoLocationsHint, LimitedResults                        string
}

func (s *Server) monitorLabels() monitorLabels {
	return monitorLabels{
		Services: s.t("Services"), Heartbeats: s.t("Heartbeats"), Resources: s.t("Resources"), Service: s.t("Service"),
		State: s.t("State"), Targets: s.t("Probe targets"), Pages: s.t("Status pages"), Severity: s.t("Severity"),
		Checked: s.t("Last checked"), Machine: s.t("Machine"), MaxAge: s.t("Maximum age"), LastSuccess: s.t("Last success"),
		Expires: s.t("Expires"), Kind: s.t("Kind"), Note: s.t("Note"), Description: s.t("Description"), None: s.t("None"),
		NoServices: s.t("No monitored services yet."), Target: s.t("Target"), Module: s.t("Module"),
		Set: s.t("Target set"), Domain: s.t("Failure domain"), Visibility: s.t("Visibility"), Alerts: s.t("Alerts"),
		Started: s.t("Started"), Window: s.t("What the probes saw in the last 30 minutes, at each location."),
		OnPages: s.t("On status pages"), Name: s.t("Name"), Add: s.t("Add HTTP monitor"), Schedule: s.t("Schedule maintenance"),
		RefreshResults: s.t("Refresh results"), MetricsDetails: s.t("Metrics error details"),
		WaitingHint:     s.t("Allow a probe cycle after saving a monitor, then refresh. If results are still missing, check the reporting locations in Overview."),
		NoLocationsHint: s.t("Assign this target to an active probe location to start checking it."),
		LimitedResults:  s.t("Some probe results are unavailable. Check again before relying on complete coverage."),
	}
}

// targetRef is one target of a service, as a target set names it.
type targetRef struct {
	Target, Module, Set, ProbedBy, Severity, Domain, Visibility, Page string
}

// pageRef is a status page's component that shows a service.
type pageRef struct{ Label, Href string }

// serviceConfig is what the configuration says of each service the probes check: its targets, and the
// status pages' components that show it.
type serviceConfig struct {
	names   []string
	targets map[string][]targetRef
	pages   map[string][]pageRef
	catalog *catalog.Catalog
}

func (s *Server) serviceConfig() serviceConfig {
	sc := serviceConfig{targets: map[string][]targetRef{}, pages: map[string][]pageRef{}}
	if s.Config == nil {
		return sc
	}
	st := s.Config.State()
	sc.catalog = st.Catalog
	ids := make([]string, 0, len(st.Catalog.Locations))
	for _, l := range st.Catalog.Locations {
		ids = append(ids, l.ID)
	}
	for _, file := range st.Targets.Files() {
		scope := targets.Scope(file, ids)
		for _, g := range st.Targets.Groups(file) {
			svc := g.Labels["service"]
			if svc == "" {
				continue
			}
			if _, ok := sc.targets[svc]; !ok {
				sc.names = append(sc.names, svc)
			}
			for _, t := range g.Targets {
				sc.targets[svc] = append(sc.targets[svc], targetRef{Target: t, Module: g.Labels["module"], Set: strings.TrimSuffix(file, ".yaml"), ProbedBy: scope,
					Severity: g.Labels["severity"], Domain: g.Labels["failure_domain"], Visibility: g.Labels["visibility"], Page: g.Labels["page"]})
			}
		}
	}
	slices.Sort(sc.names)
	for _, p := range st.Catalog.StatusPages {
		for _, c := range p.Components {
			for _, svc := range st.PageServices(c) {
				sc.pages[svc] = append(sc.pages[svc], pageRef{Label: s.pageTitle(p) + " · " + s.componentTitle(c),
					Href: consolePath("/status-pages/") + url.PathEscape(p.Name)})
			}
		}
	}
	return sc
}

func (sc serviceConfig) activeScopes(now time.Time) map[string][]string {
	byScope := map[string][]string{}
	if sc.catalog == nil {
		return byScope
	}
	for _, location := range sc.catalog.Locations {
		if !sc.catalog.Expected(location, now) {
			continue
		}
		for _, scope := range []string{"all", location.Region, location.ID} {
			byScope[scope] = append(byScope[scope], location.ID)
		}
	}
	return byScope
}

type monitorCheck struct{ location, target, module string }

type monitorChecks struct {
	expected, fresh int
	unreported      map[monitorCheck]struct{}
	results         []bridge.ProbeResult
}

// currentChecks compares the latest samples with this service's active target assignments.
// Old results from an edited endpoint, check profile or probe scope are still in the metrics store.
func currentChecks(refs []targetRef, scopes map[string][]string, results []bridge.ProbeResult) monitorChecks {
	out := monitorChecks{unreported: map[monitorCheck]struct{}{}}
	for _, ref := range refs {
		for _, location := range scopes[ref.ProbedBy] {
			out.unreported[monitorCheck{location, ref.Target, ref.Module}] = struct{}{}
		}
	}
	out.expected = len(out.unreported)
	for _, result := range results {
		check := monitorCheck{result.Location, result.Instance, result.Module}
		if _, ok := out.unreported[check]; !ok {
			continue
		}
		delete(out.unreported, check)
		out.results = append(out.results, result)
		if !result.Stale {
			out.fresh++
		}
	}
	return out
}

func (s *Server) monitorState(c monitorChecks, ps *probeState, pe bridge.ProbeEvidence) (string, string) {
	switch {
	case c.expected == 0:
		return s.t("No active probe locations"), "warning"
	case len(c.results) == 0 && pe.Error != "":
		return s.t("Probe results unavailable"), "warning"
	case len(c.results) == 0 && !pe.Truncated:
		return s.t("Waiting for results"), "info"
	case ps != nil && (len(ps.failing) > 0 || c.fresh == 0):
		return s.state(ps)
	case c.fresh < c.expected:
		return s.t("Partial coverage"), "warning"
	default:
		return s.state(ps)
	}
}

// pageTitle and componentTitle are a page's and a component's titles in the console's language.
func (s *Server) pageTitle(p catalog.StatusPage) string {
	if t := p.Title.In(string(s.Lang)); t != "" {
		return t
	}
	return p.Name
}

func (s *Server) componentTitle(c catalog.StatusComponent) string {
	if t := c.Title.In(string(s.Lang)); t != "" {
		return t
	}
	return c.Name
}

// probeState sums up what the locations see of one service: those reporting, those failing, those
// whose probes went silent, since when the oldest failure lasts and the newest result.
type probeState struct {
	reporting, failing, stale map[string]bool
	since                     *time.Time
	last                      time.Time
	reason                    string
}

// probeStates sums up pe by service; a result names its service when pe has several, else it is only's.
func probeStates(pe bridge.ProbeEvidence, only string) map[string]*probeState {
	out := map[string]*probeState{}
	for _, r := range pe.Results {
		svc := r.Service
		if svc == "" {
			svc = only
		}
		ps := out[svc]
		if ps == nil {
			ps = &probeState{reporting: map[string]bool{}, failing: map[string]bool{}, stale: map[string]bool{}}
			out[svc] = ps
		}
		if r.LastSeen.After(ps.last) {
			ps.last = r.LastSeen
		}
		if r.Stale {
			ps.stale[r.Location] = true
			continue
		}
		ps.reporting[r.Location] = true
		if r.OK {
			continue
		}
		ps.failing[r.Location] = true
		if ps.reason == "" {
			ps.reason = r.Reason
		}
		if r.FailingSince != nil && (ps.since == nil || r.FailingSince.Before(*ps.since)) {
			ps.since = r.FailingSince
		}
	}
	return out
}

// state is a service's state in words and the design system's tone: failing at two locations and at
// least half of those reporting is an outage, as the rules and the status pages count it.
func (s *Server) state(ps *probeState) (string, string) {
	switch {
	case ps == nil || (len(ps.reporting) == 0 && len(ps.stale) == 0):
		return s.t("No data"), ""
	case len(ps.reporting) == 0:
		return s.t("Stale"), "warning"
	case len(ps.failing) == 0:
		return s.t("OK"), "success"
	}
	text := s.t("Failing at %d of %d locations", len(ps.failing), len(ps.reporting))
	if len(ps.failing) >= 2 && 2*len(ps.failing) >= len(ps.reporting) {
		return text, "danger"
	}
	return text, "warning"
}

type monitorRow struct {
	Service, Href, State, Tone, Since, Reason, Target, More, Checked, Severity, SeverityTone string
	Pages                                                                                    []pageRef
	Alert, AlertHref                                                                         string
}

type heartbeatState struct {
	Name, Machine, MaxAge, Last, State, Tone, Severity, Description string
}

type resourceState struct {
	Name, Kind, Expires, Left, Tone, Note string
}

type monitorsPage struct {
	shell
	L          monitorLabels
	Rows       []monitorRow
	Heartbeats []heartbeatState
	Resources  []resourceState
	ProbeError string // why the probes could not be read
	Add        string
}

var monitorsTmpl = parse("monitors.html")

func (s *Server) monitors(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := monitorsPage{shell: s.shellFor(r, v, "monitors", s.t("Monitors")), L: s.monitorLabels()}
	if v.has("config:write") {
		p.Add = consolePath(monitorFormPath)
	}
	sc := s.serviceConfig()
	now := s.now()
	pe := s.Bridge.Probes(r.Context(), sc.names, bridge.Window{From: now.Add(-monitorWindow), To: now}, maxMonitorResults)
	if len(sc.names) > 0 {
		p.ProbeError = pe.Error
	}
	results := map[string][]bridge.ProbeResult{}
	for _, result := range pe.Results {
		svc := result.Service
		if len(sc.names) == 1 {
			svc = sc.names[0]
		}
		results[svc] = append(results[svc], result)
	}
	scopes := sc.activeScopes(now)
	firing := s.firingByService()
	failing, rows := 0, make([]monitorRow, 0, len(sc.names))
	for _, svc := range sc.names {
		ts := sc.targets[svc]
		if len(ts) == 0 {
			continue
		}
		checks := currentChecks(ts, scopes, results[svc])
		ps := probeStates(bridge.ProbeEvidence{Results: checks.results}, svc)[svc]
		row := monitorRow{Service: svc, Href: consolePath("/monitors/") + url.PathEscape(svc), Pages: sc.pages[svc]}
		row.State, row.Tone = s.monitorState(checks, ps, pe)
		if row.Tone == "danger" || row.Tone == "warning" {
			failing++
		}
		if ps != nil {
			if ps.since != nil {
				row.Since = s.t("since %s", s.short(*ps.since))
			}
			row.Reason = ps.reason
			if !ps.last.IsZero() {
				row.Checked = s.ago(ps.last)
			}
		}
		row.Target = ts[0].Target
		if len(ts) > 1 {
			row.More = s.t("and %d more", len(ts)-1)
		}
		row.Severity, row.SeverityTone = s.severity(ts[0].Severity), tone(ts[0].Severity)
		if g, ok := firing[svc]; ok {
			row.Alert, row.AlertHref = g.Title, link.AlertPath(g.ID)
		}
		rows = append(rows, row)
	}
	// the services in trouble first, then by name
	slices.SortStableFunc(rows, func(a, b monitorRow) int { return toneRank(b.Tone) - toneRank(a.Tone) })
	p.Rows = rows
	p.Count = s.t("%d services, %d in trouble", len(rows), failing)
	p.Heartbeats = s.heartbeatStates(r, now)
	p.Resources = s.resourceStates(now)
	s.render(w, monitorsTmpl, p)
}

// toneRank orders tones from the worst news.
func toneRank(t string) int {
	switch t {
	case "danger":
		return 3
	case "warning":
		return 2
	case "":
		return 1
	}
	return 0
}

// firingByService is the firing alert group of each service, the most severe and then the oldest.
func (s *Server) firingByService() map[string]bridge.GroupView {
	out := map[string]bridge.GroupView{}
	for _, g := range s.Bridge.Groups(true) {
		if g.Service == "" {
			continue
		}
		if o, ok := out[g.Service]; ok && (severityRank(o.Severity) > severityRank(g.Severity) ||
			(o.Severity == g.Severity && o.StartsAt.Before(g.StartsAt))) {
			continue
		}
		out[g.Service] = g
	}
	return out
}

func severityRank(sev string) int {
	switch sev {
	case "critical":
		return 3
	case "warning":
		return 2
	}
	return 1
}

func (s *Server) heartbeatStates(r *http.Request, now time.Time) []heartbeatState {
	if s.Config == nil {
		return nil
	}
	hbs := s.Config.Catalog().Heartbeats
	names := make([]string, 0, len(hbs))
	for _, h := range hbs {
		names = append(names, h.Name)
	}
	last, problem := s.Bridge.LastSuccess(r.Context(), names)
	out := make([]heartbeatState, 0, len(hbs))
	for _, h := range hbs {
		hs := heartbeatState{Name: h.Name, Machine: h.Machine, MaxAge: h.MaxAge.D().String(), Severity: s.severity(h.Severity), Description: h.Description}
		t, ok := last[h.Name]
		switch {
		case problem != "":
			hs.State = s.t("Unknown")
			hs.Last = problem
		case !ok:
			hs.State, hs.Tone = s.t("No success in 31 days"), "warning"
		case now.Sub(t) > h.MaxAge.D():
			hs.State, hs.Tone, hs.Last = s.t("Late"), tone(h.Severity), s.ago(t)
		default:
			hs.State, hs.Tone, hs.Last = s.t("OK"), "success", s.ago(t)
		}
		out = append(out, hs)
	}
	slices.SortStableFunc(out, func(a, b heartbeatState) int { return toneRank(b.Tone) - toneRank(a.Tone) })
	return out
}

func (s *Server) resourceStates(now time.Time) []resourceState {
	if s.Config == nil {
		return nil
	}
	rs := slices.Clone(s.Config.Catalog().Resources)
	slices.SortStableFunc(rs, func(a, b catalog.Resource) int { return a.ExpiresAt().Compare(b.ExpiresAt()) })
	out := make([]resourceState, 0, len(rs))
	for _, x := range rs {
		row := resourceState{Name: x.Name, Kind: x.Kind, Expires: x.Expires, Note: x.Note}
		days := int(x.ExpiresAt().Sub(now).Hours() / 24)
		switch {
		case x.ExpiresAt().IsZero():
		case !x.ExpiresAt().After(now):
			row.Left, row.Tone = s.t("Expired"), "danger"
		default:
			row.Left = s.t("in %d days", days)
		}
		out = append(out, row)
	}
	return out
}

// ---- one service

type monitorPage struct {
	shell
	L                      monitorLabels
	LL                     labels
	Probes                 []probeRow
	ProbesNote, ProbeError string
	Refresh, Coverage      string
	Fresh, Expected        int
	Targets                []targetRef
	Pages                  []pageRef
	Alerts                 []serviceAlert
	Schedule               string
	Edit, EditLabel        string
}

// serviceAlert is one of a service's alert groups, firing or resolved.
type serviceAlert struct {
	Title, Href, Severity, SeverityTone, Since, Status, StatusTone string
}

var monitorTmpl = parse("monitor.html")

func (s *Server) monitor(w http.ResponseWriter, r *http.Request, v *viewer) {
	svc := r.PathValue("service")
	sc := s.serviceConfig()
	ts, ok := sc.targets[svc]
	if !ok {
		s.missing(w, r, v)
		return
	}
	p := monitorPage{shell: s.shellFor(r, v, "monitors", svc, crumb{Label: s.t("Monitors"), Href: consolePath("/monitors")}),
		L: s.monitorLabels(), LL: s.labels(), Targets: ts, Pages: sc.pages[svc],
		Refresh: consolePath("/monitors/") + url.PathEscape(svc)}
	if v.has("maintenance:write") {
		p.Schedule = consolePath("/maintenance/new") + "?service=" + url.QueryEscape(svc)
	}
	if v.has("config:write") && s.Config != nil {
		if _, ok := simpleMonitor(s.Config.State(), svc); ok {
			p.Edit = consolePath("/monitors/") + url.PathEscape(svc) + "/edit"
			p.EditLabel = s.t("Edit HTTP monitor")
		}
	}
	now := s.now()
	pe := s.Bridge.Probes(r.Context(), []string{svc}, bridge.Window{From: now.Add(-monitorWindow), To: now}, maxMonitorResults)
	checks := currentChecks(ts, sc.activeScopes(now), pe.Results)
	pe.Results = checks.results
	state, tn := s.monitorState(checks, probeStates(pe, svc)[svc], pe)
	p.Badges = []badge{{Text: state, Tone: tn}}
	p.Probes = s.probes(pe, now.Add(-monitorWindow), p.LL)
	switch {
	case checks.expected == 0:
		p.ProbesNote = p.L.NoLocationsHint
	case pe.Truncated || pe.Error != "":
		p.ProbesNote = p.L.LimitedResults
	default:
		p.Fresh, p.Expected = checks.fresh, checks.expected
		p.Coverage = s.t("%d of %d checks have recent results", checks.fresh, checks.expected)
		if len(checks.unreported) > 0 {
			p.ProbesNote = p.L.WaitingHint
		}
		missing := make([]monitorCheck, 0, len(checks.unreported))
		for check := range checks.unreported {
			missing = append(missing, check)
		}
		slices.SortFunc(missing, func(a, b monitorCheck) int {
			return cmp.Or(cmp.Compare(a.location, b.location), cmp.Compare(a.target, b.target), cmp.Compare(a.module, b.module))
		})
		for _, check := range missing {
			p.Probes = append(p.Probes, probeRow{Location: check.location, Instance: check.target, Module: check.module,
				State: s.t("Waiting for results"), Tone: "info"})
		}
	}
	p.ProbeError = pe.Error
	for i := range p.Targets {
		t := &p.Targets[i]
		t.Severity = s.severity(t.Severity)
	}
	for _, g := range s.Bridge.Groups(false) {
		if g.Service != svc {
			continue
		}
		a := serviceAlert{Title: g.Title, Href: link.AlertPath(g.ID), Severity: s.severity(g.Severity), SeverityTone: tone(g.Severity),
			Since: s.short(g.StartsAt), Status: s.t("Firing"), StatusTone: "danger"}
		if g.Status == "resolved" {
			a.Status, a.StatusTone = s.t("Resolved"), "success"
		}
		p.Alerts = append(p.Alerts, a)
		if len(p.Alerts) == 20 {
			break
		}
	}
	s.render(w, monitorTmpl, p)
}
