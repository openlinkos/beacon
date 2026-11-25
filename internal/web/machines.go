package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
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
	Add, Dial, Behind, AddressHint, UserHint, CriticalMachine, NoteHint                                          string
	ConnectorKey, ConnectorHint, HostKeyOpt, HostKeyHint                                                         string
	Edit, PinAgain, PinHint, Pin, ChangeKey, ChangeKeyHint                                                       string
	RemoveMachine, RemoveHint, Confirm, Previous, NewFP                                                          string
	Note, Save, Remove                                                                                           string
	Connection, Connect, Search, SearchMachines, SearchHint, NoMatch                                             string
}

func (s *Server) machineLabels() machineLabels {
	return machineLabels{
		Name: s.t("Machine"), Address: s.t("Address"),
		Critical: s.t("Critical"), Sessions24h: s.t("Sessions in 24 h"), LastSession: s.t("Last session"),
		User: s.t("Account"), HostKey: s.t("Host key"), Added: s.t("Added"), Reachable: s.t("SSH access in your permissions"),
		Yes: s.t("Yes"), No: s.t("No"), Pause: s.t("Pause all SSH access"), Resume: s.t("Resume SSH access"),
		PauseHint:  s.t("Closes active sessions and blocks new machine operations through Beacon. Monitoring continues."),
		ResumeHint: s.t("People and agents with access can start new sessions."), Channel: s.t("The SSH channel"),
		Sessions: s.t("Recent sessions"), Commands: s.t("Commands"), None: s.t("None"), NoMachines: s.t("No machines yet."),
		Off: s.t("The SSH channel is off: ssh.listen is not set."),
		Add: s.t("Add a machine"), Dial: s.t("Beacon dials it"), Behind: s.t("Behind NAT, it connects out"),
		AddressHint:     s.t("host or host:port; 22 when the port is left out"),
		UserHint:        s.t("The account agents' certificates name; beacon when left empty."),
		CriticalMachine: s.t("Critical machine"), NoteHint: s.t("What the machine is"),
		ConnectorKey: s.t("Connect-out key"), ConnectorHint: s.t("The public key the machine signs in with (ssh-ed25519 AAAA…)."),
		HostKeyOpt: s.t("Host key, optional"), HostKeyHint: s.t("The machine's sshd host key. Left empty, Beacon learns it when the machine first connects."),
		Edit: s.t("Edit the machine"), PinAgain: s.t("Pin the host key again"),
		PinHint: s.t("Beacon connects to the machine and pins the host key it sees now."), Pin: s.t("Pin again"),
		ChangeKey: s.t("Change the connect-out key"), ChangeKeyHint: s.t("A connection made with the old key is closed. The machine reconnects with the new one."),
		RemoveMachine: s.t("Remove the machine"),
		RemoveHint:    s.t("Removing forgets the machine. Its sessions stay on record. An open connection from a machine behind NAT is closed."),
		Confirm:       s.t("Type the machine's name to confirm"), Previous: s.t("Previous fingerprint"), NewFP: s.t("New fingerprint"),
		Note: s.t("Note"), Save: s.t("Save"), Remove: s.t("Remove"),
		Connection: s.t("Connection method"), Search: s.t("Search"), SearchMachines: s.t("Search machines"),
		SearchHint: s.t("Name, address or note"), NoMatch: s.t("Nothing matches."), Connect: s.t("Connect over SSH"),
	}
}

type machineRow struct {
	Name, Href, Address, ConnectorTone, Sessions, Last, LastHref, ConnectLabel string
	Critical                                                                   bool
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

func disconnected(m sshgate.MachineView) bool {
	return m.Connector && (m.Connected == nil || !*m.Connected)
}

func (s *Server) machines(w http.ResponseWriter, r *http.Request, v *viewer) {
	ms := s.machineList(v)
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	p := machinesPage{shell: s.shellFor(r, v, "machines", s.t("Machines")), L: s.machineLabels(), Channel: s.channelSwitch(v), List: list{Query: search}}
	if search != "" {
		needle := strings.ToLower(search)
		ms = slices.DeleteFunc(ms, func(m sshgate.MachineView) bool { return !contains(needle, m.Name, m.Address, m.Note) })
	}
	p.Count = num(len(ms))
	_, from, to := s.page(len(ms), q, consolePath("/machines"), &p.List)
	ms = ms[from:to]
	if len(ms) == 0 {
		s.render(w, machinesTmpl, p)
		return
	}
	since := s.now().Add(-24 * time.Hour)
	counts, last := map[string]int{}, map[string]store.SessionRecord{}
	listed := map[string]bool{}
	for _, m := range ms {
		listed[m.Name] = true
	}
	walked := 0
	permit := s.Store.SessionAccess(v.Principal)
	s.Store.EachSession(func(rec store.SessionRecord) bool { // newest first: until every machine has its last session and the day is past
		walked++
		if listed[rec.Machine] && permit.Visible(rec) {
			if !rec.Started.Before(since) {
				counts[rec.Machine]++
			}
			if _, ok := last[rec.Machine]; !ok {
				last[rec.Machine] = rec
			}
		}
		return walked < maxScan && (len(last) < len(listed) || !rec.Started.Before(since))
	})
	for _, m := range ms {
		row := machineRow{Name: m.Name, Href: machinePath(m.Name), Critical: m.Critical, Sessions: num(counts[m.Name])}
		row.Address, row.ConnectorTone = s.connector(m)
		if v.person() && m.Reachable && !p.Channel.Paused && !disconnected(m) {
			row.ConnectLabel = s.t("Connect to %s over SSH", m.Name)
		}
		if rec, ok := last[m.Name]; ok {
			row.Last = s.t("%s by %s", s.ago(rec.Started), rec.Agent)
			if permit.Details() {
				row.LastHref = link.SessionPath(rec.ID)
			}
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

type personalConnection struct {
	Title, Hint, Href, Link, Command, Config, Setup, SetupHint string
	Copy, Copied, CopyError                                    string
}

type machinePage struct {
	shell
	L                    machineLabels
	LL                   listLabels
	Facts                []kv
	Note                 string
	List                 list
	Sessions             []sessionItem
	Commands             []commandItem
	Can                  bool
	User                 string
	Critical             bool
	Connector            bool
	EditAction           string
	ReplaceAction        string
	ConnectorAction      string
	RemoveAction         string
	OldFP, NewFP         string
	Connection           *personalConnection
	History, HistoryHref string
	MetadataVersion      int64
	SessionDetails       bool
	SessionHint          string
}

var machineTmpl = parse("machine.html")

func (p machinePage) Table() sessionTable {
	return sessionTable{L: p.LL, Rows: p.Sessions, Details: p.SessionDetails}
}

// machineConnection is the terminal hand-off for a person who may reach this machine. The gateway
// still reads that person's role when they authenticate and throughout their session.
func (s *Server) machineConnection(v *viewer, m sshgate.MachineView) *personalConnection {
	if !v.person() || !m.Reachable {
		return nil
	}
	c := &personalConnection{Title: s.t("Connect over SSH")}
	switch {
	case s.SSH.Paused():
		c.Hint = s.t("SSH access is paused for all machines.")
		c.Href, c.Link = consolePath("/machines"), s.t("View the SSH channel")
	case disconnected(m):
		c.Hint = s.t("This machine is not connected to the SSH channel yet.")
	case len(v.Account.SSHKeys) == 0:
		c.Hint = s.t("Register this device's public key in your account to connect as yourself.")
		c.Href, c.Link = consolePath("/account")+"#ssh-keys", s.t("Add an SSH key")
	default:
		c.Config = s.Bridge.PersonalSSHConfig(v.actor(), m.Name)
		if c.Config == "" {
			c.Hint = s.t("Ask the owner to set the public address of the SSH channel.")
			if v.owner() {
				c.Hint = s.t("Set ssh.public_address in Beacon's configuration to show the connection command.")
			}
			return c
		}
		c.Command = "ssh beacon-" + m.Name
		c.Hint = s.t("Beacon records the session as %s.", v.name())
		c.Setup = s.t("First-time terminal setup")
		c.SetupHint = s.t("Add each block to the file named above it, then run the SSH command in your terminal.")
		c.Copy = s.t("Copy command")
		c.Copied = s.t("Command copied")
		c.CopyError = s.t("Could not copy the command. Select it and copy it from the page.")
	}
	return c
}

func (s *Server) machine(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	var m *sshgate.MachineView
	for _, x := range s.machineList(v) {
		if x.Name == name {
			m = &x
			break
		}
	}
	if registration := r.URL.Query().Get("registration"); m == nil || registration != "" && m.Registration != registration {
		s.missing(w, r, v)
		return
	}
	l := s.machineLabels()
	p := machinePage{shell: s.shellFor(r, v, "machines", m.Name, crumb{Label: s.t("Machines"), Href: consolePath("/machines")}), L: l, LL: s.listLabels(), Note: m.Note,
		User: m.User, Critical: m.Critical, Connector: m.Connector, Connection: s.machineConnection(v, *m)}
	p.History, p.HistoryHref, p.MetadataVersion = s.t("Settings history"), machinePath(name)+"/history", m.Version
	if v.has("machines:write") {
		p.Can = true
		base := machinePath(name)
		p.EditAction, p.ReplaceAction = base+"/edit", base+"/replace"
		p.ConnectorAction, p.RemoveAction = base+"/connector", base+"/remove"
	}
	if r.URL.Query().Get("done") == "replaced" {
		p.OldFP, p.NewFP = r.URL.Query().Get("old"), r.URL.Query().Get("new")
	}
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

	permit := s.Store.SessionAccess(v.Principal)
	p.SessionDetails = permit.Details()
	recs := s.Store.Sessions(maxScan, func(rec store.SessionRecord) bool {
		return rec.Machine == name && permit.Visible(rec)
	})
	if len(recs) == 0 && machineScoped(v.Principal) {
		p.SessionHint = s.t("No sessions are available for your machine access.")
	}
	p.Count = num(len(recs))
	_, from, to := s.page(len(recs), r.URL.Query(), consolePath("/machines/")+url.PathEscape(name), &p.List)
	for i, rec := range recs[from:to] {
		p.Sessions = append(p.Sessions, s.sessionItem(rec, p.SessionDetails))
		if !p.SessionDetails || i >= 20 { // the commands of the page's 20 newest sessions
			continue
		}
		switch rec.Kind {
		case "exec", "file":
			p.Commands = append(p.Commands, commandItem{At: s.clock(rec.Started), Agent: rec.Agent, Command: rec.Command, Exit: s.exit(rec.Exit),
				Failed: rec.Exit != nil && *rec.Exit != 0, Href: link.SessionPath(rec.ID)})
		case "shell":
			tr, err := s.SSH.SessionFor(v.Principal, rec.ID)
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

// machinePath is one machine's page.
func machinePath(name string) string {
	return consolePath("/machines/") + url.PathEscape(name)
}

// machineErr sends the person back to path, naming the channel's error.
func (s *Server) machineErr(w http.ResponseWriter, r *http.Request, path string, err error) {
	code := "failed"
	switch {
	case errors.Is(err, sshgate.ErrExists):
		code = "machine.exists"
	case errors.Is(err, sshgate.ErrUnreachable):
		code = "machine.unreachable"
	case errors.Is(err, sshgate.ErrBadInput):
		code = "bad_input"
	case errors.Is(err, sshgate.ErrDisabled):
		code = "ssh.disabled"
	case errors.Is(err, sshgate.ErrNoMachine):
		code = "machine.not_found"
	case errors.Is(err, sshgate.ErrMachineVersion):
		code = "machine.version_not_found"
	case errors.Is(err, sshgate.ErrMachineChanged):
		code = "machine.changed"
	default:
		s.log().Error("console machine action failed", "err", err)
	}
	back(w, r, path, "error", code)
}

// dialSetup is what a machine Beacon dials needs, as root, before agents use it.
func dialSetup(ca, user string) string {
	return fmt.Sprintf("echo '%s' > /etc/ssh/beacon_ca.pub\n"+
		"echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf\n"+
		"id %s >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' %s\n"+
		"systemctl reload ssh\n", ca, user, user)
}

func (s *Server) addedMachine(w http.ResponseWriter, r *http.Request, v *viewer, a sshgate.Added) {
	var text, value string
	if a.Connector != nil {
		text = s.t("Machine %s was added behind NAT, as %s. It connects out to Beacon with ssh -R.", a.Machine.Name, a.Machine.User)
		switch {
		case a.Connector.HostKeyPending:
			text += " " + s.t("Its host key will be pinned when it first connects.")
		case a.Machine.HostKey != "":
			text += " " + s.t("Its host key is %s.", a.Machine.HostKey)
		}
		value = a.Connector.Script
	} else {
		text = s.t("Machine %s was added at %s as %s. Its host key is %s.", a.Machine.Name, a.Machine.Address, a.Machine.User, a.Machine.HostKey)
		value = dialSetup(a.TrustedUserCAKey, a.Machine.User)
	}
	if note := strings.TrimSpace(a.Machine.Note); note != "" {
		text += " " + s.t("Note: %s", note)
	}
	p := oncePage{shell: s.shellFor(r, v, "machines", s.t("Machine added"), crumb{Label: s.t("Machines"), Href: consolePath("/machines")}),
		Value: value, Text: text,
		Warning:  s.t("Put this on the machine, as root, before agents use it. The machines guide covers sudo and PerSourcePenaltyExemptList."),
		Back:     s.t("To the machines"),
		BackHref: consolePath("/machines")}
	s.renderStatus(w, http.StatusCreated, onceTmpl, p)
}

func (s *Server) addMachine(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := r.PostForm
	addr := strings.TrimSpace(f.Get("address"))
	if f.Get("reach") == "connector" || addr == store.ConnectorAddress {
		addr = store.ConnectorAddress
	}
	in := sshgate.MachineInput{Name: strings.TrimSpace(f.Get("name")), Address: addr, User: strings.TrimSpace(f.Get("user")),
		Critical: f.Get("critical") == "on", Note: f.Get("note")}
	if addr == store.ConnectorAddress {
		in.ConnectorKey = strings.TrimSpace(f.Get("connector_key"))
		in.HostKey = strings.TrimSpace(f.Get("host_key"))
	}
	a, err := s.SSH.Add(r.Context(), v.gate(), in)
	if err != nil {
		s.machineErr(w, r, consolePath("/machines"), err)
		return
	}
	s.addedMachine(w, r, v, a)
}

func (s *Server) editMachine(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	page := machinePath(name)
	f := r.PostForm
	note, user := f.Get("note"), strings.TrimSpace(f.Get("user"))
	critical := f.Get("critical") == "on"
	up := sshgate.MachineUpdate{Note: &note, User: &user, Critical: &critical}
	if f.Has("if_version") {
		version, err := strconv.ParseInt(f.Get("if_version"), 10, 64)
		if err != nil {
			back(w, r, page, "error", "machine.changed")
			return
		}
		up.IfVersion = &version
	}
	if _, err := s.SSH.Update(r.Context(), v.gate(), name, up); err != nil {
		s.machineErr(w, r, page, err)
		return
	}
	back(w, r, page, "done", "saved")
}

func (s *Server) replaceMachine(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	page := machinePath(name)
	var cur sshgate.MachineView
	found := false
	for _, m := range s.machineList(v) {
		if m.Name == name {
			cur, found = m, true
			break
		}
	}
	if !found {
		s.machineErr(w, r, page, fmt.Errorf("%w called %q", sshgate.ErrNoMachine, name))
		return
	}
	if cur.Connector {
		s.machineErr(w, r, page, fmt.Errorf("%w: %s connects out; pin its host key when it connects, or give host_key when adding it", sshgate.ErrBadInput, name))
		return
	}
	a, err := s.SSH.Add(r.Context(), v.gate(), sshgate.MachineInput{Name: cur.Name, Address: cur.Address, User: cur.User,
		Critical: cur.Critical, Note: cur.Note, Replace: true})
	if err != nil {
		s.machineErr(w, r, page, err)
		return
	}
	q := url.Values{"done": {"replaced"}, "old": {cur.HostKey}, "new": {a.Machine.HostKey}}
	http.Redirect(w, r, local(page)+"?"+q.Encode(), http.StatusSeeOther) //nolint:gosec // G710: local keeps the redirect on this site
}

func (s *Server) connectorMachine(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	page := machinePath(name)
	if _, err := s.SSH.SetConnector(r.Context(), v.gate(), name, r.PostForm.Get("key")); err != nil {
		s.machineErr(w, r, page, err)
		return
	}
	back(w, r, page, "done", "connector")
}

func (s *Server) removeMachine(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if r.PostForm.Get("confirm") != name {
		back(w, r, machinePath(name), "error", "confirm")
		return
	}
	if err := s.SSH.Remove(r.Context(), v.gate(), name); err != nil {
		s.machineErr(w, r, machinePath(name), err)
		return
	}
	back(w, r, consolePath("/machines"), "done", "removed")
}
