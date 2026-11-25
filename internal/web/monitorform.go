package web

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/probe"
	"github.com/openlinkos/beacon/internal/targets"
)

const monitorFormPath = "/monitors/add/"

var (
	monitorFormTmpl = parse("monitorform.html")
	monitorName     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
)

type monitorFormLabels struct {
	Name, NameHint, URL, URLHint, Scope, Module, ModuleHint, Severity, SeverityHint string
	Add, Cancel, After, AfterHint, ReviewConfig, NoModules, NoLocations, Config     string
	Remove, RemoveHint, Machine, MachineHint                                        string
}

type monitorInput struct {
	Service, URL, Scope, Module, Severity string
	Machine                               *targets.MachineBinding
}

type monitorFormPage struct {
	shell                                 `json:"-"`
	L                                     monitorFormLabels
	Input                                 monitorInput
	Version                               int64
	Form, Monitors, Config                string
	Scopes, Modules, Severities, Machines []option
	Remove                                string
}

func (s *Server) monitorForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	s.showMonitorForm(w, r, v, s.monitorDraft(), monitorInput{}, "", http.StatusOK)
}

// simpleMonitor finds a console-owned target with the shape its form can edit.
func simpleMonitor(state *configdb.State, service string) (monitorInput, bool) {
	if state == nil || !monitorName.MatchString(service) {
		return monitorInput{}, false
	}
	for _, file := range state.Targets.Files() {
		scope, ok := strings.CutSuffix(file, "-http."+service+".monitor.yaml")
		if !ok || !state.ConsoleTarget(strings.TrimSuffix(file, ".yaml")) {
			continue
		}
		g := state.Targets.Groups(file)
		if len(g) == 1 && configdb.RoutineMonitorGroup(g[0]) && g[0].Labels["service"] == service {
			return monitorInput{Service: service, Scope: scope, URL: g[0].Targets[0], Module: g[0].Labels["module"],
				Severity: g[0].Labels["severity"], Machine: g[0].Machine}, true
		}
	}
	return monitorInput{}, false
}

func (in monitorInput) set() string { return configdb.ConsoleTargetName(in.Scope, in.Service) }

func (s *Server) editMonitorForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	draft := s.monitorDraft()
	in, ok := simpleMonitor(draft.state, r.PathValue("service"))
	if !ok {
		s.missing(w, r, v)
		return
	}
	s.showMonitorForm(w, r, v, draft, in, "", http.StatusOK)
}

type monitorDraft struct {
	state   *configdb.State
	scopes  []option
	modules []string
}

func (s *Server) monitorDraft() monitorDraft {
	if s.Config == nil {
		return monitorDraft{}
	}
	state := s.Config.State()
	d := monitorDraft{state: state}
	if len(state.Catalog.Locations) > 0 {
		d.scopes = append(d.scopes, option{Value: "all", Label: s.t("All probe locations")})
	}
	regions := map[string]bool{}
	for _, loc := range state.Catalog.Locations {
		regions[loc.Region] = true
	}
	if regions["mainland"] {
		d.scopes = append(d.scopes, option{Value: "mainland", Label: s.t("Mainland probe locations")})
	}
	if regions["overseas"] {
		d.scopes = append(d.scopes, option{Value: "overseas", Label: s.t("Overseas probe locations")})
	}
	for _, loc := range state.Catalog.Locations {
		if loc.ID == "all" || slices.Contains(targets.Regions, loc.ID) {
			continue // these prefixes are already taken by the target protocol
		}
		d.scopes = append(d.scopes, option{Value: loc.ID, Label: loc.Name + " (" + loc.ID + ")"})
	}
	if ms, err := probe.ParseModules(state.Modules); err == nil {
		for name, m := range ms.Modules {
			if m.Prober == "http" && len(m.HTTP.Headers) == 0 {
				switch m.HTTP.Method {
				case "", http.MethodGet, http.MethodHead:
					d.modules = append(d.modules, name)
				}
			}
		}
		slices.Sort(d.modules)
		if i := slices.Index(d.modules, "http_2xx"); i > 0 {
			d.modules[0], d.modules[i] = d.modules[i], d.modules[0]
		}
	}
	return d
}

func monitorMachineValue(binding *targets.MachineBinding) string {
	if binding == nil {
		return ""
	}
	return binding.Name + "/" + binding.Registration
}

func (s *Server) monitorMachineSelection(v *viewer, choice string) (*targets.MachineBinding, bool) {
	if choice == "" {
		return nil, true
	}
	name, registration, valid := strings.Cut(choice, "/")
	m, enrolled := s.Store.Machine(name)
	if !valid || !enrolled || m.Registration() != registration || !v.Principal.Reaches(name) {
		return nil, false
	}
	return &targets.MachineBinding{Name: name, Registration: registration}, true
}

func (s *Server) monitorMachineOptions(v *viewer, current *targets.MachineBinding) []option {
	if !v.person() || !v.has("machines:write") {
		return nil
	}
	selected := monitorMachineValue(current)
	out := []option{{Label: s.t("No designated machine"), Selected: selected == ""}}
	for _, m := range s.Store.Machines() {
		if v.Principal.Reaches(m.Name) {
			value := m.Name + "/" + m.Registration()
			out = append(out, option{Value: value, Label: m.Name, Selected: value == selected})
		}
	}
	if current != nil && !slices.ContainsFunc(out, func(o option) bool { return o.Selected }) {
		out = append(out, option{Value: selected, Label: s.t("%s (registration unavailable)", current.Name), Selected: true})
	}
	return out
}

func (s *Server) showMonitorForm(w http.ResponseWriter, r *http.Request, v *viewer, draft monitorDraft, in monitorInput, problem string, code int) {
	heading := s.t("Add HTTP monitor")
	if r.PathValue("service") != "" {
		heading = s.t("Edit HTTP monitor")
	}
	p := monitorFormPage{
		shell: s.shellFor(r, v, "monitors", heading, crumb{Label: s.t("Monitors"), Href: consolePath("/monitors")}),
		L: monitorFormLabels{
			Name: s.t("Service name"), NameHint: s.t("Use lower-case letters, digits, dots or dashes. This name groups results and alerts."),
			URL: s.t("Endpoint URL"), URLHint: s.t("Use the full HTTP or HTTPS URL, without a query, fragment or credentials."),
			Scope: s.t("Probe from"), Module: s.t("Check profile"), ModuleHint: s.t("Choose an existing HTTP check that uses GET or HEAD without custom headers."),
			Severity: s.t("Severity"), SeverityHint: s.t("Existing alert rules use this severity. Saving a monitor does not install alert rules."),
			Machine:     s.t("Designated machine (optional)"),
			MachineHint: s.t("Choose where operators should connect when this service is down."),
			Add:         s.t("Add monitor"), Cancel: s.t("Cancel"), After: s.t("After you save"),
			AfterHint:    s.t("The selected probes pick up the new target and report their first results on its service page. A status page must be configured to show the service."),
			ReviewConfig: s.t("See probe configuration"), Config: s.t("Configuration"),
			NoModules:   s.t("Import a GET or HEAD HTTP check profile without custom headers before adding a monitor here."),
			NoLocations: s.t("Register a probe location before adding a monitor here."),
		},
		Input: in, Form: consolePath("/monitors"), Monitors: consolePath("/monitors"), Config: consolePath("/config"),
		Scopes: draft.scopes, Machines: s.monitorMachineOptions(v, in.Machine),
	}
	if svc := r.PathValue("service"); svc != "" {
		p.L.Add = s.t("Save")
		p.L.AfterHint = s.t("The new probe locations take effect on the next fetch. The service name and its history stay the same.")
		p.L.Remove = s.t("Remove monitor")
		p.L.RemoveHint = s.t("Stop this check. Its configuration history and audit records remain available.")
		p.Monitors = consolePath("/monitors/") + url.PathEscape(svc)
		p.Form = p.Monitors + "/edit"
		p.Remove = p.Monitors + "/remove"
	}
	if draft.state != nil {
		p.Version = draft.state.Version
	}
	for _, name := range draft.modules {
		p.Modules = append(p.Modules, option{Value: name, Label: name, Selected: name == in.Module})
	}
	for i := range p.Scopes {
		p.Scopes[i].Selected = p.Scopes[i].Value == in.Scope
	}
	if in.Severity == "" {
		in.Severity = "warning"
	}
	for _, sev := range []string{"warning", "critical", "info"} {
		p.Severities = append(p.Severities, option{Value: sev, Label: s.severity(sev), Selected: sev == in.Severity})
	}
	p.Problem = problem
	s.renderStatus(w, code, monitorFormTmpl, p)
}

func (s *Server) addMonitor(w http.ResponseWriter, r *http.Request, v *viewer) {
	value := func(k string) string { return strings.TrimSpace(r.PostForm.Get(k)) }
	in := monitorInput{Service: value("service"), URL: value("url"), Scope: value("scope"), Module: value("module"), Severity: value("severity")}
	draft := s.monitorDraft()
	edit := r.PathValue("service") != ""
	var previous string
	if edit {
		cur, ok := simpleMonitor(draft.state, r.PathValue("service"))
		if !ok {
			s.missing(w, r, v)
			return
		}
		previous = cur.set()
		in.Service = cur.Service
		in.Machine = cur.Machine
		if in.Scope == "" {
			in.Scope = cur.Scope // edit forms opened before the upgrade did not submit a scope
		}
	}
	bad := func(message string, code int) { s.showMonitorForm(w, r, v, draft, in, message, code) }
	version, err := strconv.ParseInt(value("version"), 10, 64)
	if !configdb.RoutineMonitorURL(in.URL) {
		in.URL = "" // do not redisplay a URL that could contain submitted credentials
	}

	switch {
	case draft.state == nil || len(draft.scopes) == 0 || len(draft.modules) == 0:
		bad(s.t("Set up a probe location and an HTTP check profile first."), http.StatusBadRequest)
		return
	case err != nil || version < 1 || version != draft.state.Version:
		bad(s.t("The configuration changed. Review the current options and save again."), http.StatusConflict)
		return
	case !monitorName.MatchString(in.Service):
		bad(s.t("Enter a service name of up to 64 characters, beginning with a lower-case letter or digit."), http.StatusBadRequest)
		return
	case in.URL == "":
		bad(s.t("Enter an HTTP or HTTPS URL without a query, fragment or credentials."), http.StatusBadRequest)
		return
	case !slices.Contains(draft.modules, in.Module) || !slices.ContainsFunc(draft.scopes, func(o option) bool { return o.Value == in.Scope }):
		bad(s.t("Choose one of the available check profiles and probe locations."), http.StatusBadRequest)
		return
	case in.Severity != "warning" && in.Severity != "critical" && in.Severity != "info":
		bad(s.t("Choose a severity."), http.StatusBadRequest)
		return
	}

	if choice := value("machine"); r.PostForm.Has("machine") && choice != monitorMachineValue(in.Machine) {
		if !v.person() || !v.has("machines:write") {
			bad(s.t("You do not have permission to change the designated machine."), http.StatusForbidden)
			return
		}
		binding, ok := s.monitorMachineSelection(v, choice)
		if !ok {
			in.Machine = nil
			bad(s.t("Choose an enrolled machine from the current list."), http.StatusBadRequest)
			return
		}
		in.Machine = binding
	}

	group := targets.Group{Targets: []string{in.URL}, Machine: in.Machine, Labels: map[string]string{
		"service": in.Service, "module": in.Module, "severity": in.Severity, "visibility": "internal",
	}}
	note, done := "Add HTTP monitor ", "monitor_added"
	if edit {
		note, done = "Edit HTTP monitor ", "saved"
	}
	_, err = s.Config.SetConsoleTargetIf(r.Context(), configdb.Actor{Name: v.name(), Via: "console"}, previous,
		in.Scope, group, note+in.Service, version)
	switch {
	case errors.Is(err, configdb.ErrConsoleTargetExists):
		bad(s.t("A target set already uses this name and scope. Choose different probe locations or review the configuration."), http.StatusConflict)
	case errors.Is(err, configdb.ErrConsoleServiceExists):
		bad(s.t("A target set already monitors this service. Review the probe configuration."), http.StatusConflict)
	case errors.Is(err, configdb.ErrConflict):
		draft = s.monitorDraft()
		bad(s.t("The configuration changed. Review the current options and save again."), http.StatusConflict)
	case errors.Is(err, configdb.ErrInvalid):
		bad(s.t("The configuration would not pass its checks after this change, so nothing changed."), http.StatusBadRequest)
	case err != nil:
		s.log().Error("console: saving a monitor failed", "err", err)
		bad(s.t("That did not work; the log says why."), http.StatusInternalServerError)
	default:
		back(w, r, consolePath("/monitors/")+url.PathEscape(in.Service), "done", done)
	}
}

func (s *Server) removeMonitor(w http.ResponseWriter, r *http.Request, v *viewer) {
	draft := s.monitorDraft()
	in, ok := simpleMonitor(draft.state, r.PathValue("service"))
	if !ok {
		s.missing(w, r, v)
		return
	}
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil || version < 1 || version != draft.state.Version {
		s.showMonitorForm(w, r, v, draft, in, s.t("The configuration changed. Review the current options and save again."), http.StatusConflict)
		return
	}
	if _, err := s.Config.DeleteConsoleTargetIf(r.Context(), configdb.Actor{Name: v.name(), Via: "console"},
		in.set(), "Remove HTTP monitor "+in.Service, version); err != nil {
		s.failed(w, r, consolePath("/monitors/")+url.PathEscape(in.Service)+"/edit", "monitor_remove", err)
		return
	}
	back(w, r, consolePath("/monitors"), "done", "monitor_removed")
}
