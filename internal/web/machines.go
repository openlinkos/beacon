package web

import (
	"net/http"
	"net/url"
	"time"

	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The machines the SSH channel reaches, with the channel's pause switch, and each machine's page: what
// it is, its recent sessions and the commands run on it.

type machineLabels struct {
	Name, Address, Critical, Sessions24h, LastSession, User, HostKey, Added                                      string
	Reachable, Yes, No, Pause, Resume, PauseHint, ResumeHint, Channel, Sessions, Commands, None, NoMachines, Off string
}

func (s *Server) machineLabels() machineLabels {
	return machineLabels{
		Name: s.t("Machine"), Address: s.t("Address"),
		Critical: s.t("Critical"), Sessions24h: s.t("Sessions in 24 h"), LastSession: s.t("Last session"),
		User: s.t("Account"), HostKey: s.t("Host key"), Added: s.t("Added"), Reachable: s.t("Your token reaches it"),
		Yes: s.t("Yes"), No: s.t("No"), Pause: s.t("Pause agents' access"), Resume: s.t("Resume agents' access"),
		PauseHint:  s.t("Pausing closes every open session and tool call at once, and turns new ones away until someone resumes."),
		ResumeHint: s.t("Agents reach machines again as soon as access resumes."), Channel: s.t("The SSH channel"),
		Sessions: s.t("Recent sessions"), Commands: s.t("Commands"), None: s.t("None"), NoMachines: s.t("No machines yet."),
		Off: s.t("The SSH channel is off: ssh.listen is not set."),
	}
}

type machineRow struct {
	Name, Href, Address, Connector, ConnectorTone, Sessions, Last, LastHref string
	Critical                                                                bool
}

type channelSwitch struct {
	On, Paused         bool
	State, Since, CSRF string
	Can                bool // the viewer may pause and resume (machines:write)
}

type machinesPage struct {
	shell
	L       machineLabels
	Channel channelSwitch
	List    list
	Rows    []machineRow
}

var machinesTmpl = parse("machines.html")

func (s *Server) channelSwitch(v *viewer) channelSwitch {
	c := channelSwitch{On: s.SSH != nil, CSRF: v.Session.CSRF, Can: v.has("machines:write")}
	switch {
	case s.SSH == nil:
	case s.SSH.Paused():
		c.Paused, c.State = true, s.t("Paused")
		c.Since = s.t("since %s (%s)", s.clock(s.SSH.PausedSince()), s.dur(s.now().Sub(s.SSH.PausedSince())))
	default:
		c.State = s.t("Running")
	}
	return c
}

func (s *Server) machineList(v *viewer) []sshgate.MachineView {
	if s.SSH == nil {
		return nil
	}
	l, err := s.SSH.List(v.gate(), "")
	if err != nil {
		return nil
	}
	return l.Machines
}

func (s *Server) connector(m sshgate.MachineView) (string, string) {
	if !m.Connector {
		return m.Address, ""
	}
	if m.Connected != nil && *m.Connected {
		return s.t("Connected since %s", s.short(m.ConnectedSince)), "success"
	}
	return s.t("Behind NAT, not connected"), "warning"
}

func (s *Server) machines(w http.ResponseWriter, r *http.Request, v *viewer) {
	ms := s.machineList(v)
	since := s.now().Add(-24 * time.Hour)
	counts, last := map[string]int{}, map[string]store.SessionRecord{}
	listed := map[string]bool{}
	for _, m := range ms {
		listed[m.Name] = true
	}
	walked := 0
	s.Store.EachSession(func(rec store.SessionRecord) bool { // newest first: until every machine has its last session and the day is past
		walked++
		if !rec.Started.Before(since) {
			counts[rec.Machine]++
		}
		if _, ok := last[rec.Machine]; !ok && listed[rec.Machine] {
			last[rec.Machine] = rec
		}
		return walked < maxScan && (len(last) < len(listed) || !rec.Started.Before(since))
	})
	p := machinesPage{shell: s.shellFor(r, v, "machines", s.t("Machines")), L: s.machineLabels(), Channel: s.channelSwitch(v)}
	p.Count = num(len(ms))
	_, from, to := s.page(len(ms), r.URL.Query(), consolePath("/machines"), &p.List)
	for _, m := range ms[from:to] {
		row := machineRow{Name: m.Name, Href: consolePath("/machines/") + url.PathEscape(m.Name), Critical: m.Critical, Sessions: num(counts[m.Name])}
		row.Address, row.ConnectorTone = s.connector(m)
		if rec, ok := last[m.Name]; ok {
			row.Last, row.LastHref = s.t("%s by %s", s.ago(rec.Started), rec.Agent), link.SessionPath(rec.ID)
		}
		p.Rows = append(p.Rows, row)
	}
	s.render(w, machinesTmpl, p)
}

// ---- one machine

type commandItem struct {
	At, Agent, Command, Exit, Took, Href string
	Failed                               bool
}

type machinePage struct {
	shell
	L        machineLabels
	LL       listLabels
	Facts    []kv
	Note     string
	List     list
	Sessions []sessionItem
	Commands []commandItem
}

var machineTmpl = parse("machine.html")

func (p machinePage) Table() sessionTable { return sessionTable{L: p.LL, Rows: p.Sessions} }

func (s *Server) machine(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	var m *sshgate.MachineView
	for _, x := range s.machineList(v) {
		if x.Name == name {
			m = &x
			break
		}
	}
	if m == nil {
		s.missing(w, r, v)
		return
	}
	l := s.machineLabels()
	p := machinePage{shell: s.shellFor(r, v, "machines", m.Name, crumb{Label: s.t("Machines"), Href: consolePath("/machines")}), L: l, LL: s.listLabels(), Note: m.Note}
	address, _ := s.connector(*m)
	if m.Critical {
		p.Badges = append(p.Badges, badge{Text: l.Critical, Tone: "danger"})
	}
	reach := l.No
	if m.Reachable {
		reach = l.Yes
	}
	p.Facts = []kv{{l.Address, address}, {l.User, m.User}, {l.HostKey, m.HostKey}, {l.Reachable, reach}}
	added := s.clock(m.Added)
	if m.AddedBy != "" {
		added = s.t("%s by %s", added, m.AddedBy)
	}
	p.Facts = append(p.Facts, kv{l.Added, added})

	var recs []store.SessionRecord
	walked := 0
	s.Store.EachSession(func(rec store.SessionRecord) bool {
		walked++
		if rec.Machine == name {
			recs = append(recs, rec)
		}
		return walked < maxScan
	})
	p.Count = num(len(recs))
	_, from, to := s.page(len(recs), r.URL.Query(), consolePath("/machines/")+url.PathEscape(name), &p.List)
	for i, rec := range recs[from:to] {
		p.Sessions = append(p.Sessions, s.sessionItem(rec))
		if i >= 20 { // the commands of the page's 20 newest sessions
			continue
		}
		switch rec.Kind {
		case "exec", "file":
			p.Commands = append(p.Commands, commandItem{At: s.clock(rec.Started), Agent: rec.Agent, Command: rec.Command, Exit: s.exit(rec.Exit),
				Failed: rec.Exit != nil && *rec.Exit != 0, Href: link.SessionPath(rec.ID)})
		case "shell":
			tr, err := s.SSH.Session(rec.ID)
			if err != nil {
				continue
			}
			for j := len(tr.Commands) - 1; j >= 0; j-- { // the newest first, as the sessions
				c := tr.Commands[j]
				p.Commands = append(p.Commands, commandItem{At: s.clock(c.Started), Agent: rec.Agent, Command: c.Command, Exit: s.exit(c.Exit),
					Took: s.took(c.Ms), Failed: c.Exit != nil && *c.Exit != 0, Href: link.SessionPath(rec.ID)})
			}
		}
	}
	s.render(w, machineTmpl, p)
}
