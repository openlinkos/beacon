package bridge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

// The evidence for an alert group, in one answer for an agent: what the probes see at each location and
// why they fail, the alert groups firing in the same failure domain, the changes around it, and what
// people and agents have done about it. The window runs from a while before the group started (30
// minutes unless asked otherwise) to now, or to 30 minutes after the group resolved.

// RangeQuerier is what the evidence needs of VictoriaMetrics besides the card's instant queries;
// *vm.Client has it.
type RangeQuerier interface {
	QueryAt(ctx context.Context, query string, at time.Time) ([]vm.Sample, error)
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]vm.Series, error)
}

const (
	defaultBefore    = 30 * time.Minute
	maxBefore        = 24 * time.Hour
	maxRelated       = 20
	maxListed        = 50 // changes and sessions
	maxListedBefore  = 20 // of those sessions, the ones on the alert's machines before it started, kept first
	maxShells        = 10 // shell sessions whose commands are read from their transcripts
	maxShellCommands = 20 // commands of each, the last ones
	maxResults       = 100
	maxServices      = 5
	staleAfter       = 3 * time.Minute // a probe silent this long is stale
	evidenceTimeout  = 5 * time.Second
)

// The phases of an alert group's story that a change, a session or a command falls in: before the group
// started (what may have caused it), while it fired, and after it resolved.
const (
	phaseBefore = "before"
	phaseDuring = "during"
	phaseAfter  = "after"
)

// phase says which phase of g's story t falls in.
func phase(g *store.Group, t time.Time) string {
	switch {
	case t.Before(g.StartsAt):
		return phaseBefore
	case !g.Firing() && !g.ResolvedAt.IsZero() && t.After(g.ResolvedAt):
		return phaseAfter
	}
	return phaseDuring
}

// machineLabels are the labels whose values may name the machine an alert is about.
var machineLabels = []string{"machine", "host", "location", "failure_domain"}

// probeMetrics are read at the end of the window, besides probe_success over it.
const probeMetrics = "probe_failure_info|probe_duration_seconds|probe_http_status_code|beacon_probe_attempts|probe_ssl_earliest_cert_expiry"

// Window is the time an answer covers.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// AlertBrief is an alert group as the evidence, the timeline and the hooks show it. Its text is made
// from labels, so it is cleaned.
type AlertBrief struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	Severity    string            `json:"severity"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Service     string            `json:"service,omitempty"`
	Services    []string          `json:"services,omitempty"` // a group of several services
	Labels      map[string]string `json:"labels"`             // the labels its alerts share
	Alerts      int               `json:"alerts"`             // alerts firing
	StartsAt    time.Time         `json:"starts_at"`
	ResolvedAt  *time.Time        `json:"resolved_at,omitempty"`
}

// AlertLinks are where to read more: the REST routes (status:read), the MCP endpoint (get_evidence,
// get_timeline and add_note take the group's id), the runbook, the dashboard, and the alert's page for
// people (Page: a signed link that needs no token, to hand to a person).
type AlertLinks struct {
	Alert     string `json:"alert"`
	Evidence  string `json:"evidence"`
	Timeline  string `json:"timeline"`
	MCP       string `json:"mcp"`
	Runbook   string `json:"runbook,omitempty"`
	Dashboard string `json:"dashboard,omitempty"`
	Page      string `json:"page,omitempty"`
}

// Evidence is what an agent needs to judge an alert group. Heartbeats are there when its labels name a
// heartbeat (HeartbeatStale, HeartbeatMissing).
type Evidence struct {
	Alert      AlertBrief      `json:"alert"`
	Window     Window          `json:"window"`
	Untrusted  Untrusted       `json:"untrusted"`
	Probes     ProbeEvidence   `json:"probes"`
	Heartbeats []HeartbeatView `json:"heartbeats,omitempty"`
	Related    []RelatedAlert  `json:"related"`
	Changes    []ChangeView    `json:"changes"` // related ones first, then the newest
	Done       Done            `json:"done"`
	Links      AlertLinks      `json:"links"`
}

// ProbeEvidence is what the probes of the group's services saw, one result per location, target and
// module. Queries are the PromQL used, for a closer look through the gateway (metrics:read).
type ProbeEvidence struct {
	Services  []string      `json:"services"`
	Locations int           `json:"locations"` // locations with results
	Failing   []string      `json:"failing"`   // locations failing at the end of the window
	Results   []ProbeResult `json:"results"`   // failing first
	Truncated bool          `json:"truncated,omitempty"`
	Queries   []string      `json:"queries,omitempty"`
	Error     string        `json:"error,omitempty"` // why the probes could not be read, or not all of them
}

// ProbeResult is one probe's state at the end of the window and its story in it. FailingSince is when
// the current failure began (FailingBeforeWindow: before the window); RecoveredAt is when a probe that
// failed in the window came back. Reasons counts the failed checks in the window by reason.
type ProbeResult struct {
	Location            string         `json:"location"`
	Service             string         `json:"service,omitempty"` // when the group has several
	Instance            string         `json:"instance"`
	Module              string         `json:"module,omitempty"`
	OK                  bool           `json:"ok"`
	Stale               bool           `json:"stale,omitempty"` // no result for over three minutes
	FailingSince        *time.Time     `json:"failing_since,omitempty"`
	FailingBeforeWindow bool           `json:"failing_before_window,omitempty"`
	RecoveredAt         *time.Time     `json:"recovered_at,omitempty"`
	Reason              string         `json:"reason,omitempty"` // probe_failure_info{reason}, while failing
	Reasons             map[string]int `json:"reasons,omitempty"`
	DurationSeconds     *float64       `json:"duration_seconds,omitempty"`
	HTTPStatus          int            `json:"http_status,omitempty"`
	Attempts            int            `json:"attempts,omitempty"` // beacon_probe_attempts
	CertExpires         *time.Time     `json:"cert_expires,omitempty"`
	LastSeen            time.Time      `json:"last_seen"`
}

// RelatedAlert is another firing alert group in the same failure domain, or of the same service.
type RelatedAlert struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Severity string    `json:"severity"`
	Service  string    `json:"service,omitempty"`
	StartsAt time.Time `json:"starts_at"`
	Acked    bool      `json:"acknowledged"`
	Why      string    `json:"why"` // failure_domain=<value>, or service
}

// ChangeView is a change event in the window; Related says whether it is about the group's service or
// one of its machines, Phase whether it came before the group started, while it fired or after.
type ChangeView struct {
	store.Change
	Related string `json:"related,omitempty"` // service or machine
	Phase   string `json:"phase"`             // before, during or after
}

// SilenceView is a silence covering the group in the window.
type SilenceView struct {
	ID       string    `json:"id"`
	By       string    `json:"by"`
	Comment  string    `json:"comment,omitempty"`
	StartsAt time.Time `json:"starts_at,omitzero"`
	EndsAt   time.Time `json:"ends_at"`
}

// SessionView is a session on one of the group's machines, or by an agent that acted on the group. Page
// is the signed link to its replay, for people. Phase says whether it began before the group started (a
// possible cause), while it fired or after it resolved. A shell's ShellCommands are the last commands run
// at its prompt, with their exit statuses, from its transcript (Commands counts them all; get_session
// has the rest and their output).
type SessionView struct {
	ID            string         `json:"id"`
	Agent         string         `json:"agent"`
	Via           string         `json:"via"`
	Machine       string         `json:"machine"`
	Kind          string         `json:"kind"`
	Command       string         `json:"command,omitempty"`
	Started       time.Time      `json:"started"`
	Ended         *time.Time     `json:"ended,omitempty"`
	Exit          *int           `json:"exit,omitempty"`
	Commands      int            `json:"commands,omitempty"` // commands run at an interactive shell's prompt
	Related       string         `json:"related"`            // machine or agent
	Phase         string         `json:"phase"`              // before, during or after
	ShellCommands []ShellCommand `json:"shell_commands,omitempty"`
	Page          string         `json:"page,omitempty"`
}

// ShellCommand is a command run at an interactive shell's prompt, as the session's transcript has it.
type ShellCommand struct {
	Command string    `json:"command"`
	Exit    *int      `json:"exit,omitempty"`
	Started time.Time `json:"started"`
}

// Done is what was done about the group. Machines are those its labels name and those the catalogue
// names for its heartbeats, whose sessions in the window (from before the group started) are listed with
// the sessions of the agents that acknowledged it or left notes.
type Done struct {
	Ack      *store.Ack        `json:"ack,omitempty"`
	Silences []SilenceView     `json:"silences"`
	Notes    []store.Note      `json:"notes"`
	Agents   []store.AgentCall `json:"agents"`
	Buzzed   *time.Time        `json:"buzzed_at,omitempty"`
	Pushed   *time.Time        `json:"pushover_at,omitempty"`
	Machines []string          `json:"machines"`
	Sessions []SessionView     `json:"sessions"`
}

// parseBefore reads how long before the group's start the window opens.
func parseBefore(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return defaultBefore, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d < 0 || d > maxBefore {
		return 0, fmt.Errorf("%w: before is a duration such as 30m or 2h, at most 24h", ErrBadInput)
	}
	return d, nil
}

func (b *Bridge) window(g *store.Group, before time.Duration) Window {
	to := b.now()
	if !g.Firing() && !g.ResolvedAt.IsZero() && g.ResolvedAt.Add(defaultBefore).Before(to) {
		to = g.ResolvedAt.Add(defaultBefore)
	}
	return Window{From: g.StartsAt.Add(-before), To: to}
}

// apiURL is the address of a path of the service, absolute when public_url is set.
func (b *Bridge) apiURL(path string) string {
	return strings.TrimSuffix(b.d.Config.PublicURL, "/") + path
}

func (b *Bridge) links(g *store.Group) AlertLinks {
	base := "/api/v1/alerts/" + g.ID
	return AlertLinks{Alert: b.apiURL(base), Evidence: b.apiURL(base + "/evidence"), Timeline: b.apiURL(base + "/timeline"),
		MCP: b.apiURL("/mcp"), Runbook: cleanURL(b.link(b.d.Config.Links.Runbook, g)), Dashboard: cleanURL(b.link(b.d.Config.Links.GrafanaService, g)),
		Page: b.AlertPage(g.ID)}
}

// groupServices are the group's service, or the services of its alerts (at most five).
func (b *Bridge) groupServices(g *store.Group) []string {
	if s := b.service(g); s != "" {
		return []string{s}
	}
	var out []string
	for _, a := range g.Alerts {
		if s := a.Labels["service"]; s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out[:min(len(out), maxServices)]
}

// labelValues are the values of a label on the group and its alerts.
func labelValues(g *store.Group, name string) []string {
	var out []string
	add := func(v string) {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	add(g.Labels[name])
	add(g.Common[name])
	for _, a := range g.Alerts {
		add(a.Labels[name])
	}
	return out
}

func (b *Bridge) brief(g *store.Group) AlertBrief {
	v := b.view(g)
	labels := maps.Clone(g.Common)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, g.Labels)
	a := AlertBrief{ID: g.ID, Status: g.Status, Severity: g.Severity, Title: cleanLine(v.Title), Description: clean(v.Description, maxLong, true),
		Service: cleanLine(v.Service), Labels: cleanLabels(labels), Alerts: v.Alerts, StartsAt: g.StartsAt, ResolvedAt: v.ResolvedAt}
	if a.Service == "" {
		for _, s := range b.groupServices(g) {
			a.Services = append(a.Services, cleanLine(s))
		}
	}
	return a
}

// Evidence gathers what an agent needs to judge the alert group id, over a window that opens before
// (30m by default, at most 24h) ahead of the group's start.
func (b *Bridge) Evidence(ctx context.Context, id, before string) (Evidence, error) {
	d, err := parseBefore(before)
	if err != nil {
		return Evidence{}, err
	}
	g, ok := b.d.Store.Group(id)
	if !ok {
		return Evidence{}, ErrNotFound
	}
	w := b.window(&g, d)
	machines := b.machines(&g)
	done := b.done(&g, w, machines)
	b.withCommands(done.Sessions)
	return Evidence{
		Alert: b.brief(&g), Window: w,
		Untrusted: untrusted("alert.title", "alert.description", "alert.service", "alert.services", "alert.labels",
			"probes.services", "probes.failing", "probes.results[].location", "probes.results[].service", "probes.results[].instance",
			"probes.results[].module", "probes.results[].reason", "probes.results[].reasons", "probes.error",
			"heartbeats[].description", "heartbeats[].error",
			"related[].title", "related[].service", "related[].why", "changes[].summary", "changes[].service", "changes[].object",
			"changes[].revision", "changes[].severity", "done.ack.note", "done.silences[].comment", "done.notes[].text",
			"done.agents[].error", "done.sessions[].command", "done.sessions[].shell_commands[].command"),
		Probes: b.probeEvidence(ctx, &g, w), Heartbeats: b.heartbeats(ctx, &g, w), Related: b.related(&g),
		Changes: b.changesAround(&g, w, machines), Done: done, Links: b.links(&g),
	}, nil
}

// ---- probes

// safeValue is a label value that can go into a query as it is: no quotes, backslashes or control
// characters, which a value from outside could use to change the query.
func safeValue(s string) bool {
	return s != "" && len(s) <= 200 && !strings.ContainsAny(s, "\"\\`") && !strings.ContainsFunc(s, unicode.IsControl)
}

// serviceSelector matches the services' series, or reports that a service cannot be queried safely.
func serviceSelector(svcs []string) (string, bool) { return labelSelector("service", svcs) }

// labelSelector matches the series whose label name has one of values, or reports that a value cannot
// be queried safely.
func labelSelector(name string, values []string) (string, bool) {
	for _, s := range values {
		if !safeValue(s) {
			return "", false
		}
	}
	if len(values) == 1 {
		return name + `="` + values[0] + `"`, true
	}
	quoted := make([]string, len(values))
	for i, s := range values {
		quoted[i] = strings.ReplaceAll(regexp.QuoteMeta(s), `\`, `\\`)
	}
	return name + `=~"` + strings.Join(quoted, "|") + `"`, true
}

// vmError is a query's error without the server's address, which is the deployment's business.
func vmError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return cleanLine(err.Error())
}

type probeKey struct{ service, location, instance, module string }

func keyOf(l map[string]string) probeKey {
	return probeKey{l["service"], l["location"], l["instance"], l["module"]}
}

func (b *Bridge) probeEvidence(ctx context.Context, g *store.Group, w Window) ProbeEvidence {
	svcs := b.groupServices(g)
	if len(svcs) == 0 {
		return ProbeEvidence{Services: []string{}, Failing: []string{}, Results: []ProbeResult{},
			Error: "the alert has no service label, so no probes belong to it"}
	}
	return b.Probes(ctx, svcs, w, maxResults)
}

// Probes is what the probes of the services svcs saw over w, as the evidence has it: one result per
// location, target and module (naming the service when there are several), failing first, at most
// limit of them (Truncated says when there were more).
func (b *Bridge) Probes(ctx context.Context, svcs []string, w Window, limit int) ProbeEvidence {
	pe := ProbeEvidence{Services: []string{}, Failing: []string{}, Results: []ProbeResult{}}
	for _, s := range svcs {
		pe.Services = append(pe.Services, cleanLine(s))
	}
	rq, ok := b.d.VM.(RangeQuerier)
	sel, safe := serviceSelector(svcs)
	switch {
	case len(svcs) == 0:
		pe.Error = "no service was asked for"
		return pe
	case !safe:
		pe.Error = "the service name cannot be put into a query safely"
		return pe
	case b.d.VM == nil:
		pe.Error = "VictoriaMetrics is not configured"
		return pe
	case !ok:
		pe.Error = "the metrics backend answers no range queries"
		return pe
	}
	ctx, cancel := context.WithTimeout(ctx, evidenceTimeout)
	defer cancel()
	span := w.To.Sub(w.From)
	step := max(30*time.Second, (span / 240).Round(time.Second))
	qSuccess := "probe_success{" + sel + "}"
	qNow := `{__name__=~"` + probeMetrics + `",` + sel + `}`
	qReasons := fmt.Sprintf("count_over_time(probe_failure_info{%s}[%ds])", sel, max(1, int(span.Seconds())))
	pe.Queries = []string{qSuccess, qNow, qReasons}
	series, err := rq.QueryRange(ctx, qSuccess, w.From, w.To, step)
	if err != nil {
		pe.Error = "VictoriaMetrics: " + vmError(err)
		return pe
	}
	results := map[probeKey]*ProbeResult{}
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		r := &ProbeResult{Location: cleanLine(s.Labels["location"]), Instance: cleanLine(s.Labels["instance"]), Module: cleanLine(s.Labels["module"])}
		if len(svcs) > 1 {
			r.Service = cleanLine(s.Labels["service"])
		}
		story(r, s.Points, w.To)
		results[keyOf(s.Labels)] = r
	}
	var errs []string
	now, err := rq.QueryAt(ctx, qNow, w.To)
	if err != nil {
		errs = append(errs, vmError(err))
	}
	for _, s := range now {
		r := results[keyOf(s.Labels)]
		if r == nil || math.IsNaN(s.Value) {
			continue
		}
		switch s.Labels["__name__"] {
		case "probe_failure_info":
			if !r.OK && s.Value > 0 {
				r.Reason = clean(s.Labels["reason"], maxShort, false)
			}
		case "probe_duration_seconds":
			d := math.Round(s.Value*1000) / 1000
			r.DurationSeconds = &d
		case "probe_http_status_code":
			if s.Value > 0 {
				r.HTTPStatus = int(s.Value)
			}
		case "beacon_probe_attempts":
			r.Attempts = int(s.Value)
		case "probe_ssl_earliest_cert_expiry":
			if s.Value > 0 {
				t := time.Unix(int64(s.Value), 0).UTC()
				r.CertExpires = &t
			}
		}
	}
	counts, err := rq.QueryAt(ctx, qReasons, w.To)
	if err != nil {
		errs = append(errs, vmError(err))
	}
	for _, s := range counts {
		r := results[keyOf(s.Labels)]
		if r == nil || s.Value < 1 {
			continue
		}
		if r.Reasons == nil {
			r.Reasons = map[string]int{}
		}
		r.Reasons[clean(s.Labels["reason"], maxShort, false)] += int(s.Value)
	}
	if len(errs) > 0 {
		pe.Error = "VictoriaMetrics: " + strings.Join(errs, "; ")
	}

	locations, failing := map[string]bool{}, map[string]bool{}
	for _, r := range results {
		pe.Results = append(pe.Results, *r)
		locations[r.Location] = true
		if !r.OK {
			failing[r.Location] = true
		}
	}
	sort.Slice(pe.Results, func(i, j int) bool {
		a, c := pe.Results[i], pe.Results[j]
		if a.OK != c.OK {
			return !a.OK
		}
		return a.Service+"\x00"+a.Location+"\x00"+a.Instance+"\x00"+a.Module < c.Service+"\x00"+c.Location+"\x00"+c.Instance+"\x00"+c.Module
	})
	if len(pe.Results) > limit {
		pe.Results, pe.Truncated = pe.Results[:limit], true
	}
	pe.Locations = len(locations)
	pe.Failing = slices.Sorted(maps.Keys(failing))
	return pe
}

// story reads a probe's success over the window: its state at the end, since when it fails, when it
// came back.
func story(r *ProbeResult, pts []vm.Point, end time.Time) {
	last := pts[len(pts)-1]
	r.OK, r.LastSeen = last.V >= 1, last.T
	r.Stale = end.Sub(last.T) > staleAfter
	if !r.OK {
		i := len(pts) - 1
		for i > 0 && pts[i-1].V < 1 {
			i--
		}
		t := pts[i].T
		r.FailingSince, r.FailingBeforeWindow = &t, i == 0
		return
	}
	for j := len(pts) - 2; j >= 0; j-- {
		if pts[j].V < 1 {
			t := pts[j+1].T
			r.RecoveredAt = &t
			return
		}
	}
}

// ---- related alerts, changes and what was done

// related lists the other firing groups that share a failure domain or a service with g.
func (b *Bridge) related(g *store.Group) []RelatedAlert {
	out := []RelatedAlert{}
	domains, svcs := labelValues(g, "failure_domain"), b.groupServices(g)
	for _, o := range b.d.Store.Groups() {
		if o.ID == g.ID || !o.Firing() {
			continue
		}
		why := ""
		for _, d := range labelValues(&o, "failure_domain") {
			if slices.Contains(domains, d) {
				why = "failure_domain=" + cleanLine(d)
				break
			}
		}
		if why == "" && slices.ContainsFunc(b.groupServices(&o), func(s string) bool { return slices.Contains(svcs, s) }) {
			why = "service"
		}
		if why == "" {
			continue
		}
		out = append(out, RelatedAlert{ID: o.ID, Title: cleanLine(b.title(&o)), Severity: o.Severity, Service: cleanLine(b.service(&o)),
			StartsAt: o.StartsAt, Acked: o.Ack != nil, Why: why})
		if len(out) == maxRelated {
			break
		}
	}
	return out
}

// machines are the machines g's labels name, and those the catalogue names for its heartbeats.
func (b *Bridge) machines(g *store.Group) []string {
	out := []string{}
	for _, l := range machineLabels {
		for _, v := range labelValues(g, l) {
			if store.MachineName.MatchString(v) && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	for _, m := range b.heartbeatMachines(g) {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// changesAround lists the change events in the window: those about the group's services or machines
// first, then the newest.
func (b *Bridge) changesAround(g *store.Group, w Window, machines []string) []ChangeView {
	svcs := b.groupServices(g)
	out := []ChangeView{}
	for _, c := range b.d.Store.Changes(store.ChangeFilter{From: w.From, To: w.To, Limit: 500}) {
		v := ChangeView{Change: c, Phase: phase(g, c.Time)}
		switch {
		case c.Service != "" && slices.Contains(svcs, c.Service):
			v.Related = "service"
		case c.Machine != "" && slices.Contains(machines, c.Machine):
			v.Related = "machine"
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Related != "" && out[j].Related == "" })
	return out[:min(len(out), maxListed)]
}

// actors are the tokens that acknowledged g or left notes on it: agents whose sessions belong to it.
func actors(g *store.Group) []string {
	var out []string
	if g.Ack != nil && (g.Ack.Via == "api" || g.Ack.Via == "mcp") {
		out = append(out, g.Ack.By)
	}
	for _, n := range g.Notes {
		if !slices.Contains(out, n.By) {
			out = append(out, n.By)
		}
	}
	return out
}

// sessions lists the latest sessions in the window, which opens before g started, on g's machines or by
// the agents that acted on it, the oldest first.
func (b *Bridge) sessions(g *store.Group, w Window, machines []string) []SessionView {
	who := actors(g)
	rs := b.d.Store.SessionsBetween(w.From, w.To, 1000, func(r store.SessionRecord) bool {
		return slices.Contains(machines, r.Machine) || slices.Contains(who, r.Agent)
	})
	rs = keepSessions(rs, g.StartsAt, machines)
	out := make([]SessionView, 0, len(rs))
	for _, r := range rs {
		v := SessionView{ID: r.ID, Agent: r.Agent, Via: r.Via, Machine: r.Machine, Kind: r.Kind, Command: cleanLine(r.Command),
			Started: r.Started, Ended: tp(r.Ended), Exit: r.Exit, Commands: r.Commands, Related: "agent", Phase: phase(g, r.Started),
			Page: b.SessionPage(r.ID)}
		if slices.Contains(machines, r.Machine) {
			v.Related = "machine"
		}
		out = append(out, v)
	}
	return out
}

// keepSessions keeps at most maxListed of rs (the oldest first), the latest ones; but the sessions on the
// alert's machines that began before it started, which may be what caused it, are kept first, up to
// maxListedBefore of them, so that the work done during the alert does not push them out.
func keepSessions(rs []store.SessionRecord, start time.Time, machines []string) []store.SessionRecord {
	if len(rs) <= maxListed {
		return rs
	}
	var before, rest []store.SessionRecord
	for _, r := range rs {
		if r.Started.Before(start) && slices.Contains(machines, r.Machine) {
			before = append(before, r)
		} else {
			rest = append(rest, r)
		}
	}
	before = before[max(0, len(before)-maxListedBefore):]
	rest = rest[max(0, len(rest)-(maxListed-len(before))):]
	out := slices.Concat(before, rest)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// withCommands adds to the shell sessions listed the commands their transcripts say were run at the
// prompt, with their exit statuses: the last maxShellCommands of each, for at most maxShells sessions,
// those before the alert first, then the latest.
func (b *Bridge) withCommands(ss []SessionView) {
	if b.d.ShellCommands == nil {
		return
	}
	order := make([]int, 0, len(ss))
	for i := range ss {
		if ss[i].Phase == phaseBefore {
			order = append(order, i)
		}
	}
	for i := len(ss) - 1; i >= 0; i-- {
		if ss[i].Phase != phaseBefore {
			order = append(order, i)
		}
	}
	n := 0
	for _, i := range order {
		s := &ss[i]
		if s.Kind != "shell" || s.Commands == 0 || s.Ended == nil {
			continue
		}
		if n++; n > maxShells {
			return
		}
		cs := b.d.ShellCommands(s.ID)
		for _, c := range cs[max(0, len(cs)-maxShellCommands):] {
			s.ShellCommands = append(s.ShellCommands, ShellCommand{Command: cleanLine(c.Command), Exit: c.Exit, Started: c.Started})
		}
	}
}

// silenceMatches reports whether every matcher of s matches labels, as the router decides.
func silenceMatches(s store.SilenceRecord, labels map[string]string) bool {
	for _, m := range s.Matchers {
		v := labels[m.Name]
		ok := v == m.Value
		if m.IsRegex {
			re, err := regexp.Compile("^(?:" + m.Value + ")$")
			ok = err == nil && re.MatchString(v)
		}
		if ok != m.IsEqual {
			return false
		}
	}
	return len(s.Matchers) > 0
}

// silences lists the silences that covered g in the window, the oldest first.
func (b *Bridge) silences(g *store.Group, w Window) []SilenceView {
	labels := maps.Clone(g.Common)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, g.Labels)
	out := []SilenceView{}
	for _, s := range b.d.Store.SilenceRecords() {
		if s.StartsAt.After(w.To) || s.EndsAt.Before(w.From) || !silenceMatches(s, labels) {
			continue
		}
		out = append(out, SilenceView{ID: s.ID, By: s.CreatedBy, Comment: clean(s.Comment, maxLong, true), StartsAt: s.StartsAt, EndsAt: s.EndsAt})
	}
	if g.Silence != nil && !slices.ContainsFunc(out, func(v SilenceView) bool { return v.ID == g.Silence.ID }) {
		out = append(out, SilenceView{ID: g.Silence.ID, By: g.Silence.By, EndsAt: g.Silence.Until})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out
}

func (b *Bridge) done(g *store.Group, w Window, machines []string) Done {
	d := Done{Silences: b.silences(g, w), Notes: []store.Note{}, Agents: []store.AgentCall{}, Buzzed: tp(g.Buzzed), Pushed: tp(g.Pushed),
		Machines: machines, Sessions: b.sessions(g, w, machines)}
	if g.Ack != nil {
		a := *g.Ack
		a.Note = clean(a.Note, maxLong, true)
		d.Ack = &a
	}
	for _, n := range g.Notes {
		n.Text = clean(n.Text, maxLong, true)
		d.Notes = append(d.Notes, n)
	}
	for _, c := range g.Agents {
		c.Error = cleanLine(c.Error)
		d.Agents = append(d.Agents, c)
	}
	return d
}
