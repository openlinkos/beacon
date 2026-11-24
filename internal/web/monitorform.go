package web

import (
	"encoding/json"
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
}

type monitorInput struct {
	Service, URL, Scope, Module, Severity string
}

type monitorFormPage struct {
	shell
	L                           monitorFormLabels
	Input                       monitorInput
	Version                     int64
	Form, Monitors, Config      string
	Scopes, Modules, Severities []option
}

func (s *Server) monitorForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	s.showMonitorForm(w, r, v, s.monitorDraft(), monitorInput{}, "", http.StatusOK)
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

func (s *Server) showMonitorForm(w http.ResponseWriter, r *http.Request, v *viewer, draft monitorDraft, in monitorInput, problem string, code int) {
	p := monitorFormPage{
		shell: s.shellFor(r, v, "monitors", s.t("Add HTTP monitor"), crumb{Label: s.t("Monitors"), Href: consolePath("/monitors")}),
		L: monitorFormLabels{
			Name: s.t("Service name"), NameHint: s.t("Use lower-case letters, digits, dots or dashes. This name groups results and alerts."),
			URL: s.t("Endpoint URL"), URLHint: s.t("Use the full HTTP or HTTPS URL, without a query, fragment or credentials."),
			Scope: s.t("Probe from"), Module: s.t("Check profile"), ModuleHint: s.t("Choose an existing HTTP check that uses GET or HEAD without custom headers."),
			Severity: s.t("Severity"), SeverityHint: s.t("Existing alert rules use this severity. Saving a monitor does not install alert rules."),
			Add: s.t("Add monitor"), Cancel: s.t("Cancel"), After: s.t("After you save"),
			AfterHint:    s.t("The selected probes pick up the new target and report their first results on its service page. A status page must be configured to show the service."),
			ReviewConfig: s.t("See probe configuration"), Config: s.t("Configuration"),
			NoModules:   s.t("Import a GET or HEAD HTTP check profile without custom headers before adding a monitor here."),
			NoLocations: s.t("Register a probe location before adding a monitor here."),
		},
		Input: in, Form: consolePath("/monitors"), Monitors: consolePath("/monitors"), Config: consolePath("/config"),
		Scopes: draft.scopes,
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
	bad := func(message string, code int) { s.showMonitorForm(w, r, v, draft, in, message, code) }
	version, err := strconv.ParseInt(value("version"), 10, 64)
	u, urlErr := url.Parse(in.URL)
	if urlErr != nil || u == nil || u.User != nil || u.RawQuery != "" || u.ForceQuery {
		in.URL = "" // do not redisplay a URL that could contain submitted credentials
		u = nil
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
	case u == nil || len(in.URL) > 2048 || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || strings.Contains(in.URL, "#"):
		in.URL = ""
		bad(s.t("Enter an HTTP or HTTPS URL without a query, fragment or credentials."), http.StatusBadRequest)
		return
	case !slices.Contains(draft.modules, in.Module) || !slices.ContainsFunc(draft.scopes, func(o option) bool { return o.Value == in.Scope }):
		bad(s.t("Choose one of the available check profiles and probe locations."), http.StatusBadRequest)
		return
	case in.Severity != "warning" && in.Severity != "critical" && in.Severity != "info":
		bad(s.t("Choose a severity."), http.StatusBadRequest)
		return
	}

	// One set per service keeps changes independently reversible. The dot before the name keeps
	// the target set's prefix from matching a longer location id.
	set := in.Scope + "-http." + in.Service + ".monitor"
	files := draft.state.Targets.Files()
	if slices.Contains(files, set+".yaml") {
		bad(s.t("A target set for this name already exists. Choose another service name."), http.StatusConflict)
		return
	}
	for _, file := range files {
		for _, group := range draft.state.Targets.Groups(file) {
			if group.Labels["service"] == in.Service {
				bad(s.t("A monitor for this service already exists. Choose another service name."), http.StatusConflict)
				return
			}
		}
	}
	groups := []targets.Group{{Targets: []string{in.URL}, Labels: map[string]string{
		"service": in.Service, "module": in.Module, "severity": in.Severity, "visibility": "internal",
	}}}
	raw, _ := json.Marshal(groups) // target groups contain only strings
	_, err = s.Config.PutIf(r.Context(), configdb.Actor{Name: v.name(), Via: "console"}, configdb.KindTargets,
		set, raw, "Add HTTP monitor "+in.Service, version)
	switch {
	case errors.Is(err, configdb.ErrConflict):
		draft = s.monitorDraft()
		bad(s.t("The configuration changed. Review the current options and save again."), http.StatusConflict)
	case errors.Is(err, configdb.ErrInvalid):
		bad(s.t("The configuration would not pass its checks after this change, so nothing changed."), http.StatusBadRequest)
	case err != nil:
		s.log().Error("console: adding a monitor failed", "err", err)
		bad(s.t("That did not work; the log says why."), http.StatusInternalServerError)
	default:
		back(w, r, consolePath("/monitors/")+url.PathEscape(in.Service), "done", "monitor_added")
	}
}
