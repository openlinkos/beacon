package web

import (
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// What the templates render. Every text people read is made here and in the console's files, through
// t, so that the catalogue test sees it; the templates hold no words of their own besides the Beacon
// name.

// labels are the texts of the alert's and the session's pages that take no arguments.
type labels struct {
	Alert, Session, Service, Services, Labels, Probes, Related, Changes, Agents, Notes, Sessions, Commands, Output, Timeline string
	Recording, Replay, Download, Machine, Via, Started, Duration, Exit, Location, Instance, Module, Result, Reason           string
	OK, Failing, Stale, Open, None, Runbook, Dashboard, NoProbes, RecordingError                                             string
	Details, Status, Severity, Kind, Ended, ID, Time, What, Acknowledge, AckNote, Silence, SilenceWhy, AddNote, Note         string
	SessionsOn, EndSilence, AckWithNote, TakeOverWithNote, ProbeResults                                                      string
	Response, EscalationStops, ProbeNow, ProbeWait, NoResult, SignIn, TakeOver                                               string
	AcknowledgementNote, FullAcknowledgementNote, LatestNote, AllNotes                                                       string
	ShareLinks, RefreshStatus, Transcript, NoEnd                                                                             string
}

func (s *Server) labels() labels {
	return labels{
		Alert: s.t("Alert"), Session: s.t("Session"), Service: s.t("Service"), Services: s.t("Services"), Labels: s.t("Labels"),
		Probes: s.t("Probes"), Related: s.t("Related alerts"), Changes: s.t("Changes"), Agents: s.t("Agents"), Notes: s.t("Notes"), Timeline: s.t("Timeline"),
		Sessions: s.t("Sessions"), Commands: s.t("Commands"), Output: s.t("Output"), Recording: s.t("Recording"), Replay: s.t("Replay"),
		Download: s.t("Download the recording"), Machine: s.t("Machine"), Via: s.t("Via"), Started: s.t("Started"),
		Duration: s.t("Duration"), Exit: s.t("Exit status"), Location: s.t("Location"), Instance: s.t("Target"), Module: s.t("Module"),
		Result: s.t("Result"), Reason: s.t("Reason"), OK: s.t("OK"), Failing: s.t("Failing"), Stale: s.t("Stale"), Open: s.t("Still open"),
		None: s.t("None"), Runbook: s.t("Runbook"), Dashboard: s.t("Dashboard"), NoProbes: s.t("No probe results in the window"),
		RecordingError: s.t("The recording could not be loaded"),
		Details:        s.t("Details"), Status: s.t("Status"), Severity: s.t("Severity"), Kind: s.t("Kind"), Ended: s.t("Ended"),
		ID: s.t("Id"), Time: s.t("Time"), What: s.t("What happened"), Acknowledge: s.t("Acknowledge"), AckNote: s.t("A note for the others (optional)"),
		AckWithNote: s.t("Acknowledge with note"), TakeOverWithNote: s.t("Take over with note"),
		Silence: s.t("Silence"), SilenceWhy: s.t("Why silence it"), AddNote: s.t("Add a note"), Note: s.t("Note"),
		SessionsOn: s.t("Sessions on the alert's machines"), EndSilence: s.t("End the silence"),
		Response: s.t("Alert response"), EscalationStops: s.t("Acknowledging pauses new escalations while the alert is acknowledged."),
		ProbeNow: s.t("Probe again now"), ProbeWait: s.t("Allow up to 45 seconds for the results."), NoResult: s.t("No result"),
		ProbeResults: s.t("On-demand check results."),
		SignIn:       s.t("Sign in"), TakeOver: s.t("Take over"),
		AcknowledgementNote: s.t("Acknowledgement note"), FullAcknowledgementNote: s.t("Read full acknowledgement note"),
		LatestNote: s.t("Latest note"), AllNotes: s.t("All notes"),
		ShareLinks: s.t("Manage share links"), RefreshStatus: s.t("Refresh status"),
		Transcript: s.t("Command transcript"), NoEnd: s.t("No end recorded"),
	}
}

// ---- helpers

func (s *Server) loc() *time.Location {
	if s.Location != nil {
		return s.Location
	}
	return time.UTC
}

func (s *Server) clock(t time.Time) string { return t.In(s.loc()).Format("2006-01-02 15:04:05") }

func (s *Server) short(t time.Time) string { return t.In(s.loc()).Format("01-02 15:04:05") }

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// dur renders a duration roughly, the way people say it.
func (s *Server) dur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return s.t("under a minute")
	case d < time.Hour:
		return s.t("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return s.t("%d h", h)
		}
		return s.t("%d h %d min", h, m)
	}
	return s.t("%d d %d h", int(d.Hours())/24, int(d.Hours())%24)
}

// took renders a command's duration, in milliseconds.
func (s *Server) took(ms int64) string {
	switch {
	case ms < 1000:
		return s.t("%d ms", ms)
	case ms < 60000:
		return s.t("%.1f s", float64(ms)/1000)
	}
	return s.dur(time.Duration(ms) * time.Millisecond)
}

func (s *Server) severity(sev string) string {
	switch sev {
	case "critical":
		return s.t("Critical")
	case "info":
		return s.t("Info")
	}
	return s.t("Warning")
}

// tone is the design system's tone for a severity or a state.
func tone(v string) string {
	switch v {
	case "critical", "firing", "failed", "paused":
		return "danger"
	case "warning", "pending":
		return "warning"
	case "info":
		return "info"
	case "resolved", "ok", "running":
		return "success"
	}
	return ""
}

func (s *Server) via(via string) string {
	switch via {
	case "feishu":
		return s.t("Feishu")
	case "pushover":
		return "Pushover"
	case "mcp":
		return "MCP"
	case "ssh":
		return "SSH"
	case "api":
		return "API"
	case "cli":
		return s.t("the command line")
	case "console":
		return s.t("the console")
	}
	return via
}

// channel names how people were told.
func (s *Server) channel(ch string) string {
	switch ch {
	case "app":
		return s.t("the Feishu app")
	case "webhook":
		return s.t("the group bot")
	case config.NotificationWebhookChannel:
		return s.t("Notification webhook")
	case config.SlackChannel:
		return "Slack"
	case "pushover":
		return "Pushover"
	case "bark":
		return "Bark"
	case "feishu-buzz":
		return s.t("Feishu urgent alerts")
	}
	return s.t("no channel")
}

func (s *Server) kind(kind string) string {
	switch kind {
	case "exec":
		return s.t("command")
	case "shell":
		return s.t("interactive shell")
	case "sftp":
		return s.t("file transfer")
	case "forward":
		return s.t("forwarded port")
	case "file":
		return s.t("file tool")
	}
	return kind
}

func (s *Server) exit(code *int) string {
	if code == nil {
		return ""
	}
	return s.t("exit %d", *code)
}

// external keeps only absolute http(s) URLs, for links that leave Beacon.
func external(u string) string {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
		return ""
	}
	return p.String()
}

// pageShell is the shell of an alert's or a session's page: the console's for someone signed in, the
// page alone for a signed link or a token.
func (s *Server) pageShell(r *http.Request, a access, nav, kind, title string, parent crumb) shell {
	if a.Viewer != nil {
		return s.shellFor(r, a.Viewer, nav, title, parent)
	}
	return s.viewOnly(r, kind, title, a)
}

// ---- the alert page

type kv struct{ K, V string }

type probeRow struct {
	Location, Service, Instance, Module string
	OK                                  bool
	State, Since, Reason, Detail, Tone  string
	Checked, CheckedISO                 string
}

type relatedRow struct {
	Title, Severity, SeverityClass, Why, Href string
	Acked                                     bool
}

type eventRow struct {
	At, ISO, Kind, Text, Detail, Href, HrefText string
	Anchor                                      string
	External                                    bool
	Probe                                       *probeEvent
}
type probeEvent struct {
	Summary, RecoveryHint string
	Open                  bool
	Rows                  []probeRow
}

type noteRow struct{ By, Via, At, ISO, Text string }

type commandRow struct {
	Command, Exit, Took string
	Failed              bool
}

type sessionRow struct {
	ID, Agent, Via, Machine, Kind, Command, Started, Duration, Exit, Href string
	Failed, Open                                                          bool
	Commands                                                              []commandRow
}

type changeRow struct {
	At, Source, Kind, Summary, About, Href string
}

// alertActions are the forms the alert's page offers someone signed in with alerts:write.
type alertActions struct {
	CSRF, Ack, Silence, Note, Probe string // where the forms post; "" leaves a form out
	Durations                       []kv   // the silence's choices: value, label
	Unsilence, Back                 string // where the form that ends the group's silence posts, and the page to come back to
	AckVersion                      string
	Episode, ReturnEpisode          string
	TakeOver                        bool
}

type alertMachineLinks struct {
	Title, Hint string
	Items       []kvLink
}

type handoverNote struct{ Preview, Full string }

type alertAgentRun struct {
	Title, Status, Tone, Exit, Anchor string
}

type alertPage struct {
	shell
	L                                                                            labels
	ID, Heading, Status, StatusClass, Severity, SeverityClass, When, Description string
	Service                                                                      string
	Ownership, Escalation                                                        string
	Feedback, FeedbackTone                                                       string
	HookState, PeopleDelivery                                                    string
	HookFailed, PeopleDeliveryFailed                                             bool
	PhoneCancellation, PhoneRefresh                                              string
	PhoneCancellationPending                                                     bool
	AgentRun                                                                     *alertAgentRun
	Handover                                                                     handoverNote
	LatestNote                                                                   *noteRow
	Services                                                                     []string
	Labels                                                                       []kv
	Runbook, Dashboard, Window                                                   string
	Probes                                                                       []probeRow
	ProbesNote, ProbesError                                                      string
	Related                                                                      []relatedRow
	Events                                                                       []eventRow
	Agents                                                                       []string
	Notes                                                                        []noteRow
	SessionsLine                                                                 string
	SessionsHint                                                                 string
	Sessions                                                                     []sessionRow
	Changes                                                                      []changeRow
	Channel                                                                      string
	Paused                                                                       bool
	Actions                                                                      *alertActions
	Machines                                                                     *alertMachineLinks
	ShareLinks, SharedViewHint                                                   string
}

func (s *Server) alertPage(r *http.Request, ev bridge.Evidence, tl bridge.Timeline, g bridge.GroupView, a access) alertPage {
	now := s.now()
	al := ev.Alert
	ref := al.ID + "." + g.Episode
	if sshIncidentPath(ref) == "" {
		ref = ""
	}
	p := alertPage{shell: s.pageShell(r, a, "alerts", s.t("Alert"), al.Title, crumb{Label: s.t("Alerts"), Href: consolePath("/alerts")}),
		L: s.labels(), ID: al.ID, Heading: al.Title, Description: al.Description,
		Service: al.Service, Services: al.Services, Severity: s.severity(al.Severity), SeverityClass: tone(al.Severity),
		Runbook: external(g.Runbook), Dashboard: external(g.Dashboard),
		Window:   s.t("Window: %s to %s", s.clock(ev.Window.From), s.clock(ev.Window.To)),
		Machines: s.alertMachines(a.Viewer, ev.LinkedMachines, ev.MonitorMachines, ref)}
	if a.Viewer.person() && a.Viewer.owner() {
		p.ShareLinks = shareLinksPath("alerts", al.ID)
	} else if a.Grant != nil {
		p.SharedViewHint = s.t("This link opens this alert. Sign in to respond or see related alerts and SSH session history.")
	}
	p.Feedback, p.FeedbackTone = p.Toast, p.ToastTone
	if p.Problem != "" {
		p.Feedback, p.FeedbackTone = p.Problem, "danger"
	}
	p.Toast, p.Problem = "", ""
	if al.Status == "firing" {
		p.Status, p.StatusClass = s.t("Firing"), "danger"
		p.When = s.t("Since %s (%s)", s.clock(al.StartsAt), s.dur(now.Sub(al.StartsAt)))
	} else {
		p.Status, p.StatusClass = s.t("Resolved"), "success"
		if al.ResolvedAt != nil {
			p.When = s.t("Resolved at %s after %s", s.clock(*al.ResolvedAt), s.dur(al.ResolvedAt.Sub(al.StartsAt)))
		}
	}
	p.Badges = []badge{{Text: p.Status, Tone: p.StatusClass}, {Text: p.Severity, Tone: p.SeverityClass}}
	keys := make([]string, 0, len(al.Labels))
	for k := range al.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.Labels = append(p.Labels, kv{k, al.Labels[k]})
	}
	if ack := g.Ack; ack != nil {
		switch {
		case ack.Via == "pushover":
			p.Ownership = s.t("Pushover reported a device acknowledgement at %s", s.clock(ack.At))
		case ack.StopsEscalation():
			p.Ownership = s.t("Acknowledged by %s at %s via %s", ack.By, s.clock(ack.At), s.via(ack.Via))
		default:
			p.Ownership = s.t("Agent acknowledgement from %s at %s via %s", ack.By, s.clock(ack.At), s.via(ack.Via))
			if al.Status == "firing" {
				p.Ownership = s.t("Awaiting a person. %s", p.Ownership)
			}
		}
	} else if al.Status == "firing" {
		p.Ownership = s.t("Awaiting human acknowledgement")
	}
	silenced := g.Silence != nil && now.Before(g.Silence.Until)
	switch {
	case silenced:
		p.Escalation = s.t("Silenced until %s by %s", s.clock(g.Silence.Until), g.Silence.By)
	case g.Status == "firing" && g.Ack.StopsEscalation():
		p.Escalation = s.t("New escalations are paused while this alert is acknowledged.")
	}
	p.PhoneCancellation, p.PhoneCancellationPending = s.pushoverCancellation(g, tl)
	if p.PhoneCancellationPending && a.Viewer != nil {
		p.PhoneRefresh = r.URL.EscapedPath()
	}
	p.HookState, p.HookFailed = s.hookState(tl, g.Status)
	p.AgentRun = s.latestAgentRun(tl, g.Status)
	if g.Status == "firing" {
		p.PeopleDelivery, p.PeopleDeliveryFailed = s.peopleDelivery(tl)
		if g.Ack.StopsEscalation() {
			p.Handover = currentHandover(tl, g.Ack)
		}
	}
	p.Probes = s.probes(ev.Probes, ev.Window.From, p.L)
	if len(p.Probes) == 0 {
		p.ProbesNote = p.L.NoProbes
	}
	p.ProbesError = ev.Probes.Error
	for _, r := range ev.Related {
		p.Related = append(p.Related, relatedRow{Title: r.Title, Severity: s.severity(r.Severity), SeverityClass: tone(r.Severity), Why: r.Why,
			Acked: r.Acked, Href: s.relatedLink(link.AlertPath(r.ID), a, "status:read")})
	}
	p.Events = s.events(tl, a, p.L, r.URL.Query().Get("probe"), g.Status == "firing")
	for _, c := range ev.Done.Agents {
		if c.Event != "firing" {
			continue
		}
		switch c.Status {
		case "ok":
			p.Agents = append(p.Agents, s.t("Alert delivered to hook %s at %s", c.Hook, s.clock(c.Done)))
		case "failed":
			p.Agents = append(p.Agents, s.t("Delivery to hook %s failed", c.Hook))
		default:
			p.Agents = append(p.Agents, s.t("Delivery to hook %s pending", c.Hook))
		}
	}
	for _, n := range ev.Done.Notes {
		p.Notes = append(p.Notes, noteRow{By: n.By, Via: s.via(n.Via), At: s.clock(n.At), ISO: iso(n.At), Text: n.Text})
	}
	slices.Reverse(p.Notes) // the latest first
	if len(p.Notes) > 0 {
		latest := p.Notes[0]
		latest.Text = shorten(strings.Join(strings.Fields(latest.Text), " "), 120)
		p.LatestNote = &latest
	}
	p.Sessions, p.SessionsLine = s.incidentSessions(ev, a)
	if machineScoped(a.Principal) {
		p.SessionsHint = s.t("Sessions shown here are limited to the machines available to this sign-in.")
	}
	for _, c := range ev.Changes {
		about := c.Service
		if c.Machine != "" {
			about = strings.TrimPrefix(about+" "+c.Machine, " ")
		}
		p.Changes = append(p.Changes, changeRow{At: s.clock(c.Time), Source: c.Source, Kind: c.Kind, Summary: c.Summary, About: about, Href: external(c.URL)})
	}
	if s.SSH != nil {
		if t := s.SSH.PausedSince(); !t.IsZero() {
			p.Channel, p.Paused = s.t("Agents paused since %s", s.short(t)), true
		} else {
			p.Channel = s.t("Agents reach machines")
		}
	}
	if v := a.Viewer; v != nil && v.has("alerts:write") {
		base := link.AlertPath(al.ID)
		p.Actions = &alertActions{CSRF: v.Session.CSRF, Note: base + "/notes", Episode: g.Episode, ReturnEpisode: r.PathValue("episode")}
		if s.Bridge.CanProbeNow() {
			p.Actions.Probe = base + "/probe-now"
		}
		if silenced {
			p.Actions.Unsilence, p.Actions.Back = consolePath("/silences/")+url.PathEscape(g.Silence.ID)+"/expire", link.IncidentPath(al.ID, p.Actions.ReturnEpisode)
		}
		if al.Status == "firing" {
			p.Actions.Silence = base + "/silence"
			p.Actions.Durations = []kv{{"1h", s.t("1 hour")}, {"4h", s.t("4 hours")}, {"24h", s.t("24 hours")}}
			if g.Ack == nil || g.Ack.Via != "console" || g.Ack.By != v.name() {
				p.Actions.Ack = base + "/ack"
				p.Actions.AckVersion = g.AckVersion
				p.Actions.TakeOver = g.Ack != nil
			}
		}
	}
	return p
}

// currentHandover uses the timeline's cleaned copy of the current acknowledgement note.
func currentHandover(tl bridge.Timeline, ack *store.Ack) handoverNote {
	for _, e := range slices.Backward(tl.Events) {
		if e.Kind == "acknowledged" && e.At.Equal(ack.At) && e.By == ack.By && e.Via == ack.Via && e.Type == ack.Kind {
			text := strings.Join(strings.Fields(e.Text), " ")
			note := handoverNote{Preview: shorten(text, 120)}
			if note.Preview != text {
				note.Full = e.Text
			}
			return note
		}
	}
	return handoverNote{}
}

func (s *Server) alertMachines(v *viewer, names []string, designated []store.MonitorMachine, ref string) *alertMachineLinks {
	if v == nil || !v.hasAny("status:read|machines:ssh") {
		return nil
	}
	out := &alertMachineLinks{Title: s.t("Linked machines")}
	if len(names) == 0 && len(designated) == 0 {
		out.Hint = s.t("No machine is linked to this alert.")
		return out
	}
	if s.SSH == nil {
		out.Hint = s.t("The SSH channel is off.")
		return out
	}
	for _, name := range names {
		m, ok := s.Store.Machine(name)
		if !ok {
			continue
		}
		href := machinePath(m.Name)
		if v.person() && v.Principal.Reaches(m.Name) {
			if ref == "" {
				href += "#ssh-connect"
			} else {
				href = incidentMachinePath(m.Name, m.Registration(), ref)
			}
		}
		out.Items = append(out.Items, kvLink{V: m.Name, K: s.t("Open machine %s", m.Name), Href: href})
	}
	if len(designated) > 0 {
		out.Title = s.t("Designated service machines")
		for _, binding := range designated {
			m, ok := s.Store.Machine(binding.Name)
			if !ok || m.Registration() != binding.Registration {
				continue
			}
			href := machinePath(m.Name) + "?registration=" + url.QueryEscape(binding.Registration)
			if v.person() && v.Principal.Reaches(m.Name) {
				if ref == "" {
					href += "#ssh-connect"
				} else {
					href = incidentMachinePath(m.Name, binding.Registration, ref)
				}
			}
			out.Items = append(out.Items, kvLink{V: m.Name + " (" + binding.Service + ")",
				K: s.t("Open designated machine %s for %s", m.Name, binding.Service), Href: href})
		}
		if len(out.Items) < len(designated) {
			out.Hint = s.t("A designated machine registration is no longer available.")
		}
	} else if len(out.Items) == 0 {
		out.Hint = s.t("This alert links to machines that are not enrolled.")
	}
	return out
}

// probes are the rows of what the probes saw over a window that began at from.
func (s *Server) probes(pe bridge.ProbeEvidence, from time.Time, l labels) []probeRow {
	var out []probeRow
	for _, r := range pe.Results {
		row := probeRow{Location: r.Location, Service: r.Service, Instance: r.Instance, Module: r.Module, OK: r.OK, Reason: r.Reason}
		if !r.LastSeen.IsZero() {
			row.Checked, row.CheckedISO = s.short(r.LastSeen), iso(r.LastSeen)
		}
		switch {
		case r.Stale:
			row.State, row.Tone = l.Stale, "warning"
		case r.OK:
			row.State, row.Tone = l.OK, "success"
		default:
			row.State, row.Tone = l.Failing, "danger"
		}
		switch {
		case !r.OK && r.FailingBeforeWindow:
			row.Since = s.t("since before %s", s.clock(from))
		case !r.OK && r.FailingSince != nil:
			row.Since = s.t("since %s", s.clock(*r.FailingSince))
		case r.OK && r.RecoveredAt != nil:
			row.Since = s.t("recovered at %s", s.clock(*r.RecoveredAt))
		}
		var parts []string
		if r.DurationSeconds != nil {
			parts = append(parts, s.t("%.2f s", *r.DurationSeconds))
		}
		if r.HTTPStatus > 0 {
			parts = append(parts, s.t("HTTP %d", r.HTTPStatus))
		}
		if r.Attempts > 1 {
			parts = append(parts, s.t("%d attempts", r.Attempts))
		}
		if r.CertExpires != nil {
			parts = append(parts, s.t("certificate expires %s", s.clock(*r.CertExpires)))
		}
		row.Detail = strings.Join(parts, ", ")
		out = append(out, row)
	}
	return out
}

// events renders the timeline.
func (s *Server) events(tl bridge.Timeline, a access, l labels, selectedProbe string, firing bool) []eventRow {
	out := make([]eventRow, 0, len(tl.Events))
	reports := make(map[string]bool)
	for _, e := range tl.Events {
		if e.Kind == "agent_run" && e.ID != "" {
			reports[e.ID] = true
		}
	}
	for _, e := range tl.Events {
		row := eventRow{At: s.short(e.At), ISO: iso(e.At), Kind: e.Kind}
		switch e.Kind {
		case "started":
			row.Text = s.t("Alert started")
		case "notified":
			row.Text = s.t("Notification accepted by %s", s.channel(e.Via))
		case "buzzed":
			row.Text = s.t("Feishu accepted the urgent alert")
		case "pushover":
			row.Text = s.t("Pushover accepted the page")
		case "notification":
			if e.Via == "pushover" && e.Event == "cancel" && (e.Result == "ok" || e.Result == "failed") {
				if e.Result == "ok" {
					row.Text = s.t("Pushover confirmed page cancellation")
				} else {
					row.Text = s.t("Pushover cancellation attempt failed")
				}
				break
			}
			if e.Event == "resolved" {
				if e.Result == "ok" {
					row.Text = s.t("Recovery update accepted by %s", s.channel(e.Via))
				} else {
					row.Text = s.t("Recovery update submission to %s failed", s.channel(e.Via))
				}
				break
			}
			switch e.Result {
			case "ok":
				row.Text = s.t("Notification accepted by %s", s.channel(e.Via))
			case "failed":
				row.Text = s.t("Notification submission to %s failed", s.channel(e.Via))
			default:
				row.Text = s.t("Notification attempt via %s recorded", s.channel(e.Via))
			}
		case "acknowledged":
			row.Text, row.Detail = s.t("Acknowledged by %s via %s", e.By, s.via(e.Via)), e.Text
			if e.Via == "pushover" {
				row.Text = s.t("Pushover reported a device acknowledgement")
			} else if e.Type == "agent" {
				row.Text = s.t("Agent acknowledgement from %s via %s", e.By, s.via(e.Via))
			}
		case "note":
			row.Text, row.Detail = s.t("%s left a note via %s", e.By, s.via(e.Via)), e.Text
		case "silenced":
			until := ""
			if e.Until != nil {
				until = s.clock(*e.Until)
			}
			row.Text, row.Detail = s.t("Silenced by %s until %s", e.By, until), e.Text
		case "silence_ended":
			row.Text = s.t("Silence ended")
		case "agent":
			row.Text, row.Detail = s.agentEvent(e)
			if e.Result == "ok" && !reports[e.ID] {
				row.Detail = s.t("No Agent program report recorded.")
			}
		case "agent_run":
			run := s.agentRun(e)
			row.Text, row.Detail = run.Title, run.Status
			if run.Exit != "" {
				row.Detail += " (" + run.Exit + ")"
			}
			row.Anchor = run.Anchor
		case "probe_now":
			row.Text = s.t("%s ran an on-demand check via %s", e.By, s.via(e.Via))
			row.Anchor = "probe-" + e.ID
			row.Probe = s.probeEvent(e, l, e.ID == selectedProbe, firing)
		case "change":
			row.Text, row.Detail = s.t("Change by %s", e.By), e.Text
			if e.Type != "" {
				row.Text = s.t("Change by %s (%s)", e.By, e.Type)
			}
			if u := external(e.URL); u != "" {
				row.Href, row.HrefText, row.External = u, u, true
			}
		case "session":
			row.Text = s.t("%s on %s", e.By, e.Machine)
			row.Detail = s.kind(e.Type)
			if a.SessionDetail && e.Text != "" {
				row.Detail = s.t("%s: %s", row.Detail, e.Text)
			}
			if x := s.exit(e.Exit); x != "" {
				row.Detail += " (" + x + ")"
			}
			if e.ID != "" {
				row.Href, row.HrefText = s.relatedLink(link.SessionPath(e.ID), a, "sessions:read|machines:ssh"), l.Replay
			}
		case "command": // one run at a shell's prompt, within its session
			row.Text = s.t("%s ran a command on %s", e.By, e.Machine)
			if a.SessionDetail {
				row.Detail = e.Text
			}
			if x := s.exit(e.Exit); x != "" {
				row.Detail = strings.TrimSpace(row.Detail + " (" + x + ")")
			}
			if e.ID != "" {
				row.Href, row.HrefText = s.relatedLink(link.SessionPath(e.ID), a, "sessions:read|machines:ssh"), l.Replay
			}
		case "heartbeat": // its last success, when the story began
			row.Text, row.Detail = s.t("Heartbeat %s last succeeded", e.ID), e.Text
		case "resolved":
			row.Text = s.t("Alert resolved")
		default:
			row.Text, row.Detail = e.Kind, e.Text
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) hookState(tl bridge.Timeline, phase string) (string, bool) {
	var pending, failed bool
	for _, e := range tl.Events {
		if e.Kind != "agent" || e.Event != phase {
			continue
		}
		switch e.Result {
		case "ok":
			if phase == "resolved" {
				return s.t("A hook accepted the recovery notice."), false
			}
			return s.t("A hook accepted the alert."), false
		case "failed":
			failed = true
		default:
			pending = true
		}
	}
	switch {
	case pending:
		return s.t("Agent hook delivery pending."), false
	case failed:
		return s.t("Agent hook delivery failed."), true
	case phase == "firing":
		return s.t("No Agent hook was called."), false
	default:
		return "", false
	}
}

// latestAgentRun prefers the alert's current phase. A prior report stays visible when recovery has
// no execution report or accepted wake of its own.
func (s *Server) latestAgentRun(tl bridge.Timeline, phase string) *alertAgentRun {
	var prior *alertAgentRun
	accepted := false
	for _, e := range slices.Backward(tl.Events) {
		switch {
		case e.Kind == "agent_run" && e.Event == phase:
			run := s.agentRun(e)
			return &run
		case e.Kind == "agent" && e.Event == phase && e.Result == "ok":
			accepted = true
		case e.Kind == "agent_run" && e.Event == "firing" && phase == "resolved" && prior == nil:
			run := s.agentRun(e)
			prior = &run
		}
	}
	if accepted {
		return &alertAgentRun{Title: s.t("No Agent program report recorded.")}
	}
	return prior
}

func (s *Server) agentRun(e bridge.Event) alertAgentRun {
	run := alertAgentRun{Exit: s.exit(e.Exit)}
	switch e.Event {
	case "firing":
		run.Title = s.t("Agent program for the alert trigger (%s)", e.By)
	case "resolved":
		run.Title = s.t("Agent program for the recovery notice (%s)", e.By)
	default:
		run.Title = s.t("Agent program (%s)", e.By)
	}
	switch e.Result {
	case "running":
		run.Status, run.Tone = s.t("Running"), "info"
	case "succeeded":
		run.Status = s.t("Completed")
	case "failed":
		run.Status, run.Tone = s.t("Failed"), "danger"
	case "timed_out":
		run.Status, run.Tone = s.t("Timed out"), "danger"
	case "not_started":
		run.Status, run.Tone = s.t("Did not start"), "danger"
	default:
		run.Status = s.t("Status reported")
	}
	if e.ID != "" {
		run.Anchor = "agent-run-" + e.ID + "-" + e.Result
	}
	return run
}

func (s *Server) peopleDelivery(tl bridge.Timeline) (string, bool) {
	seen := map[string]bool{}
	var accepted string
	for _, e := range slices.Backward(tl.Events) {
		if e.Event == "cancel" || e.Event == "resolved" {
			continue
		}
		var route string
		switch e.Kind {
		case "notified", "notification", "pushover":
			route = e.Via
		case "buzzed":
			route = "feishu-buzz"
		default:
			continue
		}
		if seen[route] {
			continue
		}
		seen[route] = true
		if e.Result == "failed" {
			return s.t("Notification submission to %s failed", s.channel(route)), true
		}
		if accepted == "" && e.Result == "ok" {
			accepted = s.t("Notification accepted by %s", s.channel(route))
		}
	}
	if accepted == "" {
		return s.t("No people notification acceptance recorded."), false
	}
	return accepted, false
}

func (s *Server) pushoverCancellation(g bridge.GroupView, tl bridge.Timeline) (string, bool) {
	switch g.PushoverCancellation {
	case "pending":
		return s.t("Pushover cancellation pending. The earlier page may still repeat."), true
	case "confirmed":
		return s.t("Pushover confirmed cancellation of the page."), false
	case "expired":
		return s.t("The retry window for the Pushover page has ended."), false
	case "unconfirmed":
		return s.t("Pushover cancellation could not be verified."), false
	}
	if g.Pushed == nil || (g.Status != "resolved" && !g.Ack.StopsEscalation()) || (g.Ack != nil && g.Ack.Via == "pushover") {
		return "", false
	}
	// A device response for this page can remain in the timeline after a person takes over.
	for _, e := range tl.Events {
		if e.Kind == "acknowledged" && e.Via == "pushover" && !e.At.Before(*g.Pushed) {
			return "", false
		}
	}
	return s.t("No cancellation result is recorded for the Pushover page."), false
}

func (s *Server) agentEvent(e bridge.Event) (text, detail string) {
	switch e.Result {
	case "ok":
		if e.Event == "resolved" {
			return s.t("Recovery notice delivered to hook %s", e.By), ""
		}
		return s.t("Alert delivered to hook %s", e.By), ""
	case "failed":
		return s.t("Delivery to hook %s failed", e.By), e.Text
	}
	return s.t("Delivery to hook %s pending", e.By), ""
}

func (s *Server) probeEvent(e bridge.Event, l labels, open, firing bool) *probeEvent {
	p := &probeEvent{Open: open}
	var passed, failed, missing int
	for _, o := range e.Probes {
		r := probeRow{Location: o.Location, Instance: o.Instance, Module: o.Module, Reason: o.Reason}
		switch o.Status {
		case "ok":
			r.State, r.Tone = l.OK, "success"
			passed++
		case "failed":
			r.State, r.Tone = l.Failing, "danger"
			failed++
		default:
			r.State, r.Tone = l.NoResult, "warning"
			missing++
		}
		if o.HTTPCode > 0 {
			r.Detail = s.t("HTTP %d", o.HTTPCode)
		}
		p.Rows = append(p.Rows, r)
	}
	p.Summary = s.t("%d passed, %d failed, %d missing", passed, failed, missing)
	if open && firing && passed > 0 && passed == len(e.Probes) {
		p.RecoveryHint = s.t("The monitoring rule still reports this alert as firing.")
	}
	return p
}

// sessions renders the sessions on the alert's machines, each with the commands of its transcript.
func (s *Server) sessions(views []bridge.SessionView, a access) []sessionRow {
	out := make([]sessionRow, 0, len(views))
	for _, v := range views {
		row := sessionRow{ID: v.ID, Agent: v.Agent, Via: s.via(v.Via), Machine: v.Machine, Kind: s.kind(v.Kind),
			Started: s.clock(v.Started), Exit: s.exit(v.Exit), Failed: v.Exit != nil && *v.Exit != 0, Open: v.Ended == nil,
			Href: s.relatedLink(link.SessionPath(v.ID), a, "sessions:read|machines:ssh")}
		if v.Ended != nil {
			row.Duration = s.dur(v.Ended.Sub(v.Started))
		}
		if a.SessionDetail {
			row.Command = v.Command
			if v.Kind == "shell" && s.SSH != nil {
				if tr, err := s.SSH.SessionFor(a.Principal, v.ID); err == nil {
					row.Commands = s.commands(tr.Commands)
				}
			}
		}
		out = append(out, row)
	}
	slices.Reverse(out) // the newest first, as people look for the latest
	return out
}

func (s *Server) commands(cs []sshgate.Command) []commandRow {
	out := make([]commandRow, 0, len(cs))
	for _, c := range cs {
		out = append(out, commandRow{Command: c.Command, Exit: s.exit(c.Exit), Took: s.took(c.Ms), Failed: c.Exit != nil && *c.Exit != 0})
	}
	return out
}

// ---- the session page

type sessionPage struct {
	shell
	L                                                                        labels
	ActorLabel                                                               string
	ID, Agent, Via, Machine, Kind, Command, Started, Ended, Duration, Exit   string
	Failed, Open                                                             bool
	Commands                                                                 []commandRow
	Outputs                                                                  []kv // stdout and stderr of a command
	Recording, RecordingNote, RecordingReadNote, TranscriptNote, MachineHref string
	RecordingState, TranscriptState                                          badge
	ShareLinks, SharedViewHint                                               string
}

func (s *Server) sessionActorKind(principal string) string {
	kind, _, ok := strings.Cut(principal, ":")
	if !ok {
		return ""
	}
	switch kind {
	case "agent":
		return s.t("Agent")
	case "user":
		return s.t("Person")
	case "token":
		return s.t("Token")
	default:
		return ""
	}
}

func (s *Server) sessionPage(r *http.Request, tr sshgate.Transcript, a access) sessionPage {
	rec := tr.SessionRecord
	heading := s.t("%s on %s", rec.Agent, rec.Machine)
	p := sessionPage{shell: s.pageShell(r, a, "sessions", s.t("Session"), heading, crumb{Label: s.t("Sessions"), Href: consolePath("/sessions")}),
		L: s.labels(), ID: rec.ID, Agent: rec.Agent, Via: s.via(rec.Via), Machine: rec.Machine,
		Kind: s.kind(rec.Kind), Command: rec.Command, Started: s.clock(rec.Started), Exit: s.exit(rec.Exit),
		Failed: rec.Exit != nil && *rec.Exit != 0, Open: rec.Ended.IsZero(), Commands: s.commands(tr.Commands)}
	if a.Viewer.person() && a.Viewer.owner() {
		p.ShareLinks = shareLinksPath("sessions", rec.ID)
	} else if a.Grant != nil {
		p.SharedViewHint = s.t("This link opens this session and any available recording. Sign in to open other sessions or machines you can access.")
	}
	p.ActorLabel = s.sessionActorKind(rec.Principal)
	if p.ActorLabel == "" {
		p.ActorLabel = s.t("Actor")
	}
	p.Badges = []badge{{Text: p.Kind}}
	switch {
	case p.Open:
		p.Badges = append(p.Badges, badge{Text: p.L.NoEnd, Tone: "info"})
	case p.Exit != "" && p.Failed:
		p.Badges = append(p.Badges, badge{Text: p.Exit, Tone: "danger"})
	case p.Exit != "":
		p.Badges = append(p.Badges, badge{Text: p.Exit, Tone: "success"})
	}
	if a.Viewer != nil {
		p.MachineHref = consolePath("/machines/") + url.PathEscape(rec.Machine)
	}
	if !rec.Ended.IsZero() {
		p.Ended, p.Duration = s.clock(rec.Ended), s.dur(rec.Ended.Sub(rec.Started))
	}
	if tr.Stdout != "" {
		p.Outputs = append(p.Outputs, kv{"stdout", tr.Stdout})
	}
	if tr.Stderr != "" {
		p.Outputs = append(p.Outputs, kv{"stderr", tr.Stderr})
	}
	recording, transcript := sessionEvidence(rec)
	p.RecordingState, p.TranscriptState = s.sessionEvidenceBadge(recording), s.sessionEvidenceBadge(transcript)
	p.RecordingNote = s.recordingHint(recording)
	p.TranscriptNote = s.transcriptHint(rec, transcript, len(p.Commands) == 0 && len(p.Outputs) == 0)
	if tr.RecordingAvailable != nil && *tr.RecordingAvailable {
		p.Recording = s.recordingLink(link.RecordingPath(rec.ID), a)
	} else if !p.Open && (recording == store.EvidenceComplete || recording == store.EvidenceTruncated || recording == "") {
		p.RecordingReadNote = s.t("The recording cannot be opened from storage.")
	}
	return p
}
