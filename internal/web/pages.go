package web

import (
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/web/link"
)

// What the templates render. Every text people read is made here and in the console's files, through
// t, so that the catalogue test sees it; the templates hold no words of their own besides the Beacon
// name.

// labels are the texts of the alert's and the session's pages that take no arguments.
type labels struct {
	Alert, Session, Service, Services, Labels, Probes, Related, Changes, Agents, Notes, Sessions, Commands, Output, Timeline string
	Recording, Replay, Download, Machine, Agent, Via, Started, Duration, Exit, Location, Instance, Module, Result, Reason    string
	OK, Failing, Stale, Open, None, Runbook, Dashboard, NoProbes, NoRecording, RecordingOpen, RecordingCut, RecordingError   string
	Details, Status, Severity, Kind, Ended, ID, Time, What, Acknowledge, AckNote, Silence, SilenceWhy, AddNote, Note         string
	SessionsOn                                                                                                               string
}

func (s *Server) labels() labels {
	return labels{
		Alert: s.t("Alert"), Session: s.t("Session"), Service: s.t("Service"), Services: s.t("Services"), Labels: s.t("Labels"),
		Probes: s.t("Probes"), Related: s.t("Related alerts"), Changes: s.t("Changes"), Agents: s.t("Agents"), Notes: s.t("Notes"), Timeline: s.t("Timeline"),
		Sessions: s.t("Sessions"), Commands: s.t("Commands"), Output: s.t("Output"), Recording: s.t("Recording"), Replay: s.t("Replay"),
		Download: s.t("Download the recording"), Machine: s.t("Machine"), Agent: s.t("Agent"), Via: s.t("Via"), Started: s.t("Started"),
		Duration: s.t("Duration"), Exit: s.t("Exit status"), Location: s.t("Location"), Instance: s.t("Target"), Module: s.t("Module"),
		Result: s.t("Result"), Reason: s.t("Reason"), OK: s.t("OK"), Failing: s.t("Failing"), Stale: s.t("Stale"), Open: s.t("Still open"),
		None: s.t("None"), Runbook: s.t("Runbook"), Dashboard: s.t("Dashboard"), NoProbes: s.t("No probe results in the window"),
		NoRecording: s.t("No recording"), RecordingOpen: s.t("The session is still open; the recording plays once it ends"),
		RecordingCut: s.t("The recording was cut at 16 MiB"), RecordingError: s.t("The recording could not be loaded"),
		Details: s.t("Details"), Status: s.t("Status"), Severity: s.t("Severity"), Kind: s.t("Kind"), Ended: s.t("Ended"),
		ID: s.t("Id"), Time: s.t("Time"), What: s.t("What happened"), Acknowledge: s.t("Acknowledge"), AckNote: s.t("A note for the others (optional)"),
		Silence: s.t("Silence"), SilenceWhy: s.t("Why silence it"), AddNote: s.t("Add a note"), Note: s.t("Note"),
		SessionsOn: s.t("Sessions on the alert's machines"),
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
	case "pushover":
		return "Pushover"
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
	return s.viewOnly(kind, title, a)
}

// ---- the alert page

type kv struct{ K, V string }

type probeRow struct {
	Location, Service, Instance, Module string
	OK                                  bool
	State, Since, Reason, Detail        string
}

type relatedRow struct {
	Title, Severity, SeverityClass, Why, Href string
	Acked                                     bool
}

type eventRow struct {
	At, ISO, Kind, Text, Detail, Href, HrefText string
	External                                    bool
}

type noteRow struct{ By, Via, At, Text string }

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
	CSRF, Ack, Silence, Note string // where the forms post; "" leaves a form out
	Durations                []kv   // the silence's choices: value, label
}

type alertPage struct {
	shell
	L                                                                            labels
	ID, Heading, Status, StatusClass, Severity, SeverityClass, When, Description string
	Service                                                                      string
	Services                                                                     []string
	Labels                                                                       []kv
	State                                                                        []string // acknowledged, silenced, buzzed, paged
	Runbook, Dashboard, Window                                                   string
	Probes                                                                       []probeRow
	ProbesNote, ProbesError                                                      string
	Related                                                                      []relatedRow
	Events                                                                       []eventRow
	Agents                                                                       []string
	Notes                                                                        []noteRow
	SessionsLine                                                                 string
	Sessions                                                                     []sessionRow
	Changes                                                                      []changeRow
	Channel                                                                      string
	Paused                                                                       bool
	Actions                                                                      *alertActions
}

func (s *Server) alertPage(r *http.Request, ev bridge.Evidence, tl bridge.Timeline, g bridge.GroupView, a access) alertPage {
	now := s.now()
	al := ev.Alert
	p := alertPage{shell: s.pageShell(r, a, "alerts", s.t("Alert"), al.Title, crumb{Label: s.t("Alerts"), Href: consolePath("/alerts")}),
		L: s.labels(), ID: al.ID, Heading: al.Title, Description: al.Description,
		Service: al.Service, Services: al.Services, Severity: s.severity(al.Severity), SeverityClass: tone(al.Severity),
		Runbook: external(g.Runbook), Dashboard: external(g.Dashboard),
		Window: s.t("Window: %s to %s", s.clock(ev.Window.From), s.clock(ev.Window.To))}
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
	if d := ev.Done; d.Ack != nil {
		p.State = append(p.State, s.t("Acknowledged by %s at %s via %s", d.Ack.By, s.clock(d.Ack.At), s.via(d.Ack.Via)))
	}
	if g.Silence != nil && now.Before(g.Silence.Until) {
		p.State = append(p.State, s.t("Silenced until %s by %s", s.clock(g.Silence.Until), g.Silence.By))
	}
	if ev.Done.Buzzed != nil {
		p.State = append(p.State, s.t("Buzzed in Feishu at %s", s.clock(*ev.Done.Buzzed)))
	}
	if ev.Done.Pushed != nil {
		p.State = append(p.State, s.t("Pushover alert sent at %s", s.clock(*ev.Done.Pushed)))
	}
	p.Probes = s.probes(ev, p.L)
	if len(p.Probes) == 0 {
		p.ProbesNote = p.L.NoProbes
	}
	p.ProbesError = ev.Probes.Error
	for _, r := range ev.Related {
		p.Related = append(p.Related, relatedRow{Title: r.Title, Severity: s.severity(r.Severity), SeverityClass: tone(r.Severity), Why: r.Why,
			Acked: r.Acked, Href: s.signed(link.AlertPath(r.ID), a.Until)})
	}
	p.Events = s.events(tl, a, p.L)
	for _, c := range ev.Done.Agents {
		if c.Event != "firing" {
			continue
		}
		switch c.Status {
		case "ok":
			p.Agents = append(p.Agents, s.t("Agent %s woken at %s", c.Hook, s.clock(c.Done)))
		case "failed":
			p.Agents = append(p.Agents, s.t("Could not wake agent %s", c.Hook))
		default:
			p.Agents = append(p.Agents, s.t("Waking agent %s", c.Hook))
		}
	}
	for _, n := range ev.Done.Notes {
		p.Notes = append(p.Notes, noteRow{By: n.By, Via: s.via(n.Via), At: s.clock(n.At), Text: n.Text})
	}
	slices.Reverse(p.Notes) // the latest first
	p.Sessions = s.sessions(ev.Done.Sessions, a)
	if n := len(p.Sessions); n > 0 {
		machines, commands := map[string]bool{}, 0
		for _, v := range ev.Done.Sessions {
			machines[v.Machine] = true
		}
		for i, v := range ev.Done.Sessions {
			switch v.Kind {
			case "shell":
				commands += max(v.Commands, len(p.Sessions[len(p.Sessions)-1-i].Commands))
			case "exec", "file":
				commands++
			}
		}
		p.SessionsLine = s.t("%d sessions on %d machines, %d commands", n, len(machines), commands)
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
			p.Channel, p.Paused = s.t("Agents' access to machines is paused since %s", s.clock(t)), true
		} else {
			p.Channel = s.t("Agents reach machines through the SSH channel")
		}
	}
	if v := a.Viewer; v != nil && v.has("alerts:write") {
		base := link.AlertPath(al.ID)
		p.Actions = &alertActions{CSRF: v.Session.CSRF, Note: base + "/notes"}
		if al.Status == "firing" {
			p.Actions.Silence = base + "/silence"
			p.Actions.Durations = []kv{{"1h", s.t("Silence for 1 hour")}, {"4h", s.t("Silence for 4 hours")}, {"24h", s.t("Silence for 24 hours")}}
			if ev.Done.Ack == nil {
				p.Actions.Ack = base + "/ack"
			}
		}
	}
	return p
}

func (s *Server) probes(ev bridge.Evidence, l labels) []probeRow {
	var out []probeRow
	for _, r := range ev.Probes.Results {
		row := probeRow{Location: r.Location, Service: r.Service, Instance: r.Instance, Module: r.Module, OK: r.OK, Reason: r.Reason}
		switch {
		case r.Stale:
			row.State = l.Stale
		case r.OK:
			row.State = l.OK
		default:
			row.State = l.Failing
		}
		switch {
		case !r.OK && r.FailingBeforeWindow:
			row.Since = s.t("since before %s", s.clock(ev.Window.From))
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
func (s *Server) events(tl bridge.Timeline, a access, l labels) []eventRow {
	out := make([]eventRow, 0, len(tl.Events))
	for _, e := range tl.Events {
		row := eventRow{At: s.short(e.At), ISO: iso(e.At), Kind: e.Kind}
		switch e.Kind {
		case "started":
			row.Text = s.t("Alert started")
		case "notified":
			row.Text = s.t("People notified via %s", s.channel(e.Via))
		case "buzzed":
			row.Text = s.t("Buzzed in Feishu")
		case "pushover":
			row.Text = s.t("Pushover alert sent")
		case "acknowledged":
			row.Text, row.Detail = s.t("Acknowledged by %s via %s", e.By, s.via(e.Via)), e.Text
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
			if e.Text != "" {
				row.Detail = s.t("%s: %s", row.Detail, e.Text)
			}
			if x := s.exit(e.Exit); x != "" {
				row.Detail += " (" + x + ")"
			}
			if e.ID != "" {
				row.Href, row.HrefText = s.signed(link.SessionPath(e.ID), a.Until), l.Replay
			}
		case "command": // one run at a shell's prompt, within its session
			row.Text, row.Detail = s.t("%s ran a command on %s", e.By, e.Machine), e.Text
			if x := s.exit(e.Exit); x != "" {
				row.Detail += " (" + x + ")"
			}
			if e.ID != "" {
				row.Href, row.HrefText = s.signed(link.SessionPath(e.ID), a.Until), l.Replay
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

func (s *Server) agentEvent(e bridge.Event) (text, detail string) {
	if e.Event == "resolved" {
		switch e.Result {
		case "ok":
			return s.t("Agent %s told that the alert resolved", e.By), ""
		case "failed":
			return s.t("Could not reach agent %s", e.By), e.Text
		}
		return s.t("Telling agent %s", e.By), ""
	}
	switch e.Result {
	case "ok":
		return s.t("Agent %s woken", e.By), ""
	case "failed":
		return s.t("Could not wake agent %s", e.By), e.Text
	}
	return s.t("Waking agent %s", e.By), ""
}

// sessions renders the sessions on the alert's machines, each with the commands of its transcript.
func (s *Server) sessions(views []bridge.SessionView, a access) []sessionRow {
	out := make([]sessionRow, 0, len(views))
	for _, v := range views {
		row := sessionRow{ID: v.ID, Agent: v.Agent, Via: s.via(v.Via), Machine: v.Machine, Kind: s.kind(v.Kind), Command: v.Command,
			Started: s.clock(v.Started), Exit: s.exit(v.Exit), Failed: v.Exit != nil && *v.Exit != 0, Open: v.Ended == nil,
			Href: s.signed(link.SessionPath(v.ID), a.Until)}
		if v.Ended != nil {
			row.Duration = s.dur(v.Ended.Sub(v.Started))
		}
		if v.Kind == "shell" && s.SSH != nil {
			if tr, err := s.SSH.Session(v.ID); err == nil {
				row.Commands = s.commands(tr.Commands)
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
	L                                                                      labels
	ID, Agent, Via, Machine, Kind, Command, Started, Ended, Duration, Exit string
	Failed, Open, Truncated                                                bool
	Commands                                                               []commandRow
	Outputs                                                                []kv // stdout and stderr of a command
	Recording, RecordingNote, MachineHref                                  string
}

func (s *Server) sessionPage(r *http.Request, tr sshgate.Transcript, a access) sessionPage {
	rec := tr.SessionRecord
	heading := s.t("%s on %s", rec.Agent, rec.Machine)
	p := sessionPage{shell: s.pageShell(r, a, "sessions", s.t("Session"), heading, crumb{Label: s.t("Sessions"), Href: consolePath("/sessions")}),
		L: s.labels(), ID: rec.ID, Agent: rec.Agent, Via: s.via(rec.Via), Machine: rec.Machine,
		Kind: s.kind(rec.Kind), Command: rec.Command, Started: s.clock(rec.Started), Exit: s.exit(rec.Exit),
		Failed: rec.Exit != nil && *rec.Exit != 0, Open: rec.Ended.IsZero(), Truncated: tr.Truncated, Commands: s.commands(tr.Commands)}
	p.Badges = []badge{{Text: p.Kind}}
	switch {
	case p.Open:
		p.Badges = append(p.Badges, badge{Text: p.L.Open, Tone: "info"})
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
	switch {
	case rec.Recording == "":
		p.RecordingNote = p.L.NoRecording
	case p.Open:
		p.RecordingNote = p.L.RecordingOpen
	default:
		p.Recording = s.signed(link.RecordingPath(rec.ID), a.Until)
	}
	return p
}
