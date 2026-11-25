package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
)

const wallTimeFormat = "2006-01-02T15:04"

var maintenanceFormTmpl = parse("maintenanceform.html")

type maintenanceFormLabels struct {
	Title, TitleHint, Description, Services, ServicesHint, Starts, Ends, Timezone              string
	Preview, Publish, Edit, Cancel, Effect, EffectHint, Public, NoPublic, NoServices, Monitors string
}

type maintenanceDraft struct {
	Title, Description, Start, End string
	Services                       []string
}

type maintenancePreview struct {
	Title, Description, Starts, Ends string
	Pages                            []pageRef
}

type maintenanceFormPage struct {
	shell
	L                        maintenanceFormLabels
	Form, Silences, Monitors string
	Draft                    maintenanceDraft
	Services                 []option
	Version                  int64
	Preview                  *maintenancePreview
}

func (s *Server) maintenanceForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	in := maintenanceDraft{Services: []string{r.URL.Query().Get("service")}}
	s.renderMaintenance(w, r, v, s.maintenanceConfig(), in, nil, "", http.StatusOK)
}

func (s *Server) maintenanceConfig() *configdb.State {
	if s.Config == nil {
		return nil
	}
	return s.Config.State()
}

func configuredServices(st *configdb.State) []string {
	if st == nil || st.Targets == nil {
		return nil
	}
	names := map[string]bool{}
	for _, f := range st.Targets.Files() {
		for _, g := range st.Targets.Groups(f) {
			if svc := g.Labels["service"]; svc != "" {
				names[svc] = true
			}
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func (s *Server) renderMaintenance(w http.ResponseWriter, r *http.Request, v *viewer, st *configdb.State, in maintenanceDraft,
	preview *maintenancePreview, problem string, code int,
) {
	p := maintenanceFormPage{
		shell: s.shellFor(r, v, "silences", s.t("Schedule maintenance"), crumb{Label: s.t("Silences and maintenance"), Href: consolePath("/silences")}),
		L: maintenanceFormLabels{
			Title: s.t("Maintenance title"), TitleHint: s.t("The title and description may appear on public status pages."),
			Description: s.t("Description (optional)"), Services: s.t("Affected services"),
			ServicesHint: s.t("Select only the services you want to silence during the window."),
			Starts:       s.t("Starts"), Ends: s.t("Ends"),
			Timezone: s.t("Times are in %s. Leave the start empty to begin immediately.", s.loc().String()),
			Preview:  s.t("Preview maintenance"), Publish: s.t("Schedule maintenance"), Edit: s.t("Edit details"), Cancel: s.t("Cancel"),
			Effect: s.t("Effect on alerts"), EffectHint: s.t("Alerts that name these services are silenced during the window. Probes keep running."),
			Public: s.t("Public announcement"), NoPublic: s.t("No current public status page shows these services."),
			NoServices: s.t("There are no monitored services to schedule maintenance for yet."), Monitors: s.t("View monitors"),
		},
		Form: consolePath("/maintenance"), Silences: consolePath("/silences"), Monitors: consolePath("/monitors"),
		Draft: in, Preview: preview,
	}
	if st != nil {
		p.Version = st.Version
		for _, svc := range configuredServices(st) {
			p.Services = append(p.Services, option{Value: svc, Label: svc, Selected: slices.Contains(in.Services, svc)})
		}
	}
	p.Problem = problem
	s.renderStatus(w, code, maintenanceFormTmpl, p)
}

// maintenanceTime accepts the local time shown on the form. A clock reading skipped for daylight
// saving is invalid; the preview includes the resolved offset to disambiguate repeated hours.
func (s *Server) maintenanceTime(value string) (time.Time, bool) {
	t, err := time.ParseInLocation(wallTimeFormat, value, s.loc())
	return t, err == nil && t.Format(wallTimeFormat) == value
}

func (s *Server) planMaintenance(w http.ResponseWriter, r *http.Request, v *viewer) {
	in := maintenanceDraft{
		Title: strings.TrimSpace(r.PostForm.Get("title")), Description: strings.TrimSpace(r.PostForm.Get("description")),
		Start: r.PostForm.Get("start"), End: r.PostForm.Get("end"), Services: r.PostForm["service"],
	}
	st := s.maintenanceConfig()
	show := func(preview *maintenancePreview, message string, code int) {
		s.renderMaintenance(w, r, v, st, in, preview, message, code)
	}
	intent := r.PostForm.Get("intent")
	if intent == "edit" {
		show(nil, "", http.StatusOK)
		return
	}

	names := configuredServices(st)
	if len(in.Services) == 0 || slices.ContainsFunc(in.Services, func(s string) bool { return !slices.Contains(names, s) }) {
		show(nil, s.t("Select one or more of the monitored services."), http.StatusBadRequest)
		return
	}
	start, startOK := s.maintenanceTime(in.Start)
	end, endOK := s.maintenanceTime(in.End)
	if (in.Start != "" && (!startOK || !start.After(s.now()))) || !endOK || (in.Start != "" && !end.After(start)) || !end.After(s.now()) {
		show(nil, s.t("Choose a future end after the start. Leave the start empty to begin immediately."), http.StatusBadRequest)
		return
	}
	input := bridge.MaintenanceInput{Title: in.Title, Description: in.Description, StartsAt: start, EndsAt: end, Services: in.Services}
	next, err := s.Bridge.PreviewMaintenance(input)
	if err != nil {
		show(nil, s.t("Enter a title, select up to 50 services and choose a window of up to 7 days within the next 90 days."), http.StatusBadRequest)
		return
	}
	preview := s.maintenancePreview(st, next, in.Start == "")
	switch {
	case intent != "schedule":
		show(preview, "", http.StatusOK)
		return
	case r.PostForm.Get("version") != strconv.FormatInt(st.Version, 10):
		show(preview, s.t("The configuration changed. Check the affected pages and schedule again."), http.StatusConflict)
		return
	}
	if _, err := s.Bridge.ScheduleMaintenance(r.Context(), v.actor(), input); err != nil {
		if errors.Is(err, bridge.ErrBadInput) {
			show(nil, s.t("The window is no longer valid. Check the start and end times."), http.StatusBadRequest)
			return
		}
		s.log().Error("console: scheduling maintenance failed", "err", err)
		show(preview, s.t("That did not work; the log says why."), http.StatusInternalServerError)
		return
	}
	back(w, r, consolePath("/silences"), "done", "maintenance_scheduled")
}

func (s *Server) maintenancePreview(st *configdb.State, in bridge.MaintenanceInput, now bool) *maintenancePreview {
	format := "2006-01-02 15:04 (UTC-07:00)"
	p := &maintenancePreview{
		Title: in.Title, Description: in.Description, Starts: in.StartsAt.In(s.loc()).Format(format),
		Ends: in.EndsAt.In(s.loc()).Format(format),
	}
	if now {
		p.Starts = s.t("Immediately after scheduling")
	}
	for _, page := range st.Catalog.StatusPages {
		href := "/status/" + url.PathEscape(page.Name)
		for _, component := range page.Components {
			if services := st.PageServices(component); slices.ContainsFunc(services, func(s string) bool { return slices.Contains(in.Services, s) }) {
				p.Pages = append(p.Pages, pageRef{Label: s.pageTitle(page) + " · " + s.componentTitle(component), Href: href})
			}
		}
	}
	return p
}
