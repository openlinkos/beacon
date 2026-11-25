package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/targets"
)

type monitorTargetLabels struct {
	Scope, Domain, Page, Visibility, Machine string
}

func (s *Server) monitorTargetLabels() monitorTargetLabels {
	return monitorTargetLabels{Scope: s.t("Probe from"),
		Domain: s.t("Failure domain"), Page: s.t("Page label"), Visibility: s.t("Visibility"), Machine: s.t("Designated machine")}
}

type monitorTargetGroup struct {
	L                                                            monitorTargetLabels
	Name, Set, Scope, Module, Severity, Domain, Page, Visibility string
	Targets                                                      []string
	Machine, Edit, EditLabel                                     string
}

func (s *Server) monitorTargetGroup(st *configdb.State, set string, index int, g targets.Group) monitorTargetGroup {
	ids := make([]string, 0, len(st.Catalog.Locations))
	for _, location := range st.Catalog.Locations {
		ids = append(ids, location.ID)
	}
	return monitorTargetGroup{
		L: s.monitorTargetLabels(), Name: s.t("Group %d", index+1), Set: set, Scope: targets.Scope(set+".yaml", ids), Module: g.Labels["module"],
		Severity: s.severity(g.Labels["severity"]), Domain: g.Labels["failure_domain"], Page: g.Labels["page"], Visibility: g.Labels["visibility"], Targets: g.Targets,
	}
}

func canChangeResponseMachine(v *viewer, binding *targets.MachineBinding) bool {
	return v.person() && v.has("config:write") && v.has("machines:write") &&
		(binding == nil || v.Principal.Reaches(binding.Name))
}

func monitorDetailPath(service string) string {
	return consolePath("/monitors/") + url.PathEscape(service)
}

func responseMachinePath(service, set string, index int) string {
	return monitorDetailPath(service) + "/response-machine/" + url.PathEscape(set) + "/" + strconv.Itoa(index)
}

func (s *Server) monitorTargetGroups(st *configdb.State, v *viewer, svc string) []monitorTargetGroup {
	type locator struct {
		set   string
		index int
	}
	allowed := map[locator]bool{}
	for _, group := range st.ImportedHTTPGroups(svc) {
		allowed[locator{group.Set, group.Index}] = true
	}
	var groups []monitorTargetGroup
	for _, file := range st.Targets.Files() {
		set := strings.TrimSuffix(file, ".yaml")
		for i, group := range st.Targets.Groups(file) {
			if group.Labels["service"] != svc {
				continue
			}
			row := s.monitorTargetGroup(st, set, i, group)
			if binding := group.Machine; binding != nil && v.Principal.Reaches(binding.Name) {
				row.Machine = binding.Name
				if m, ok := s.Store.Machine(binding.Name); !ok || m.Registration() != binding.Registration {
					row.Machine = s.t("%s (registration unavailable)", binding.Name)
				}
			}
			if allowed[locator{set, i}] && canChangeResponseMachine(v, group.Machine) {
				row.Edit = responseMachinePath(svc, set, i) + "?version=" + strconv.FormatInt(st.Version, 10)
				row.EditLabel = s.t("Change designated machine for %s, group %d", set, i+1)
			}
			groups = append(groups, row)
		}
	}
	return groups
}

type responseMachinePage struct {
	shell                     `json:"-"`
	Back, Action              string
	Return, Save, Designation string
	Hint, GroupHint           string
	Version                   int64
	Group                     *monitorTargetGroup
	Machines                  []option
}

var responseMachineTmpl = parse("response_machine.html")

func (s *Server) responseMachine(w http.ResponseWriter, r *http.Request, v *viewer) {
	if !v.person() {
		s.message(w, r, v, http.StatusForbidden, s.t("Change designated machine"), s.t("You do not have permission to change the designated machine."))
		return
	}
	if !v.has("machines:write") {
		s.forbidden(w, r, v, "machines:write")
		return
	}
	if s.Config == nil {
		s.missing(w, r, v)
		return
	}
	svc, set := r.PathValue("service"), r.PathValue("set")
	index, err := strconv.Atoi(r.PathValue("group"))
	if err != nil || index < 0 {
		s.missing(w, r, v)
		return
	}
	st := s.Config.State()
	post := r.Method == http.MethodPost
	versionValue := r.URL.Query().Get("version")
	if post {
		versionValue = r.PostForm.Get("version")
	}
	version, err := strconv.ParseInt(versionValue, 10, 64)
	heading := s.t("Change designated machine")
	detail := monitorDetailPath(svc)
	p := responseMachinePage{
		shell: s.shellFor(r, v, "monitors", heading, crumb{Label: s.t("Monitors"), Href: consolePath("/monitors")}, crumb{Label: svc, Href: detail}),
		Back:  detail, Action: responseMachinePath(svc, set, index),
		Return: s.t("Back to monitor"), Save: s.t("Save designation"), Designation: s.t("Designated machine (optional)"),
		Hint:      s.t("Choose where operators should connect when this service is down."),
		GroupHint: s.t("When an alert covers several groups, the alert links to a machine only if those groups agree on its enrolment."),
		Version:   version,
	}
	show := func(status int, problem string) {
		p.Problem = problem
		s.renderStatus(w, status, responseMachineTmpl, p)
	}
	stale := s.t("The configuration changed. Open the monitor and choose the target group again.")
	if err != nil || version < 1 || version != st.Version {
		show(http.StatusConflict, stale)
		return
	}
	for _, group := range st.ImportedHTTPGroups(svc) {
		if group.Set != set || group.Index != index {
			continue
		}
		if !canChangeResponseMachine(v, group.Group.Machine) {
			s.message(w, r, v, http.StatusForbidden, heading, s.t("You do not have permission to change the designated machine."))
			return
		}
		entry := s.monitorTargetGroup(st, set, index, group.Group)
		p.Group = &entry
		p.Machines = s.monitorMachineOptions(v, group.Group.Machine)
		break
	}
	if p.Group == nil {
		s.missing(w, r, v)
		return
	}
	if !post {
		show(http.StatusOK, "")
		return
	}
	chosen := r.PostForm["machine"]
	if len(chosen) != 1 {
		show(http.StatusBadRequest, s.t("Choose an enrolled machine from the current list."))
		return
	}
	binding, ok := s.monitorMachineSelection(v, chosen[0])
	if !ok {
		show(http.StatusBadRequest, s.t("Choose an enrolled machine from the current list."))
		return
	}
	_, err = s.Config.SetTargetMachineIf(r.Context(), configdb.Actor{Name: v.name(), Via: "console"}, set, index,
		binding, "Change designated machine for "+svc, version)
	switch {
	case errors.Is(err, configdb.ErrConflict), errors.Is(err, configdb.ErrNotFound):
		p.Group = nil
		show(http.StatusConflict, stale)
	case errors.Is(err, configdb.ErrBadInput), errors.Is(err, configdb.ErrInvalid):
		show(http.StatusBadRequest, s.t("The configuration would not pass its checks after this change, so nothing changed."))
	case err != nil:
		s.log().Error("console: saving designated machine", "err", err)
		show(http.StatusInternalServerError, s.t("That did not work; the log says why."))
	default:
		back(w, r, detail, "done", "saved")
	}
}
