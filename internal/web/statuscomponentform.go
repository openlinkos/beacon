package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
)

type statusComponentLabels struct {
	Name, NameHint, Title, Services, ServiceHint, Automatic, Preview, Publish, Edit, Cancel string
	PublicEffect, Added, Removed, Visible, NoChange, AllNotes, Remove, Destination          string
	NoServices, PublicHint, RemoveHint, Maintenance, Starts, Ends                           string
}

type statusComponentPreview struct {
	Title                   string
	Added, Removed, Visible []string
	AllNotes                bool
	Announcements           []maintenanceAnnouncement
	Confirmation            string
	Revision                *uint64
}

type maintenanceAnnouncement struct {
	Title, Description, Starts, Ends, Status string
}

type statusComponentFormPage struct {
	shell
	L                                                 statusComponentLabels
	Form, Page, PublicURL, ComponentTitle, Name, Mode string
	Version                                           int64
	Choices                                           []option
	Automatic                                         []string
	Preview                                           *statusComponentPreview
}

var statusComponentFormTmpl = parse("statuscomponentform.html")

func statusComponentPath(page, name, mode string) string {
	base := consolePath("/status-pages/") + url.PathEscape(page) + "/components/"
	if mode == "add" {
		return base + "add"
	}
	return base + url.PathEscape(name) + "/" + mode
}

func (s *Server) statusComponentForm(mode string) func(http.ResponseWriter, *http.Request, *viewer) {
	return func(w http.ResponseWriter, r *http.Request, v *viewer) {
		if s.Config == nil || s.Status == nil {
			s.missing(w, r, v)
			return
		}
		state := s.Config.State()
		page, ok := state.Catalog.StatusPage(r.PathValue("name"))
		if !ok {
			s.missing(w, r, v)
			return
		}
		component, found := page.Component(r.PathValue("component"))
		if mode != "add" && !found {
			s.missing(w, r, v)
			return
		}
		draft := configdb.StatusComponentEdit{Component: component.Name, Services: component.Services, Remove: mode == "remove"}
		show := func(preview *statusComponentPreview, problem string, code int) {
			s.renderStatusComponent(w, r, v, state, page, component, draft, mode, preview, problem, code)
		}
		if r.Method == http.MethodGet {
			show(nil, "", http.StatusOK)
			return
		}

		version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
		if err != nil || version < 1 || version != state.Version {
			show(nil, s.t("The status page configuration changed. Review the current services and preview again."), http.StatusConflict)
			return
		}
		if mode == "add" {
			draft.Component, draft.Title = strings.TrimSpace(r.PostForm.Get("component")), strings.TrimSpace(r.PostForm.Get("title"))
		}
		if mode != "remove" {
			draft.Services = r.PostForm["service"]
		}
		intent := r.PostForm.Get("intent")
		if intent == "edit" {
			show(nil, "", http.StatusOK)
			return
		}
		if mode == "add" && draft.Title == "" {
			show(nil, s.statusComponentProblem(mode), http.StatusBadRequest)
			return
		}
		next, err := state.EditStatusComponent(page.Name, draft)
		if err != nil {
			show(nil, s.statusComponentProblem(mode), http.StatusBadRequest)
			return
		}
		if intent != "preview" && intent != "publish" {
			show(nil, s.t("Preview the change or publish it."), http.StatusBadRequest)
			return
		}
		title := component.Title.In(string(s.Lang))
		if mode == "add" {
			title = draft.Title
		}
		preview, err := s.statusComponentPreview(state, page, next, title)
		if err != nil {
			s.log().Error("console: reading maintenance for a public preview", "err", err)
			show(nil, s.t("The public maintenance announcements could not be checked. Try again."), http.StatusServiceUnavailable)
			return
		}
		if intent == "preview" {
			show(&preview, "", http.StatusOK)
			return
		}
		confirmation := r.PostForm.Get("confirmation")
		if (len(preview.Announcements) > 0 || confirmation != "") && confirmation != preview.Confirmation {
			show(&preview, s.t("Review the maintenance announcements below before publishing."), http.StatusConflict)
			return
		}
		err = s.saveStatusComponent(r, v, next, draft.Component, version, preview.Revision)
		switch {
		case errors.Is(err, store.ErrMaintenanceChanged):
			// Do not reuse a previous result when a source changed as the transaction began.
			refreshed, err := s.statusComponentPreview(state, page, next, title)
			if err != nil {
				show(nil, s.t("The public maintenance announcements could not be checked. Try again."), http.StatusServiceUnavailable)
				return
			}
			show(&refreshed, s.t("Review the maintenance announcements below before publishing."), http.StatusConflict)
		case errors.Is(err, configdb.ErrConflict):
			title := s.t("Configuration changed")
			s.message(w, r, v, http.StatusConflict, title, s.t("Open the status page again and preview the change using the latest services."))
		case err != nil:
			s.failed(w, r, consolePath("/status-pages/")+url.PathEscape(page.Name), "status component", err)
		default:
			back(w, r, consolePath("/status-pages/")+url.PathEscape(page.Name), "done", "status_page_updated")
		}
	}
}

func (s *Server) statusComponentPreview(state *configdb.State, before, after catalog.StatusPage, title string) (statusComponentPreview, error) {
	preview := statusComponentEffect(state, before, after)
	preview.Title = title
	if len(preview.Added) == 0 {
		return preview, nil
	}
	windows, revision, err := s.Status.MaintenancePublication(state, before, after)
	if err != nil {
		return preview, err
	}
	review := struct {
		Version int64
		Page    catalog.StatusPage
		Windows []status.Window
	}{state.Version, after, windows}
	data, err := json.Marshal(review)
	if err != nil {
		return preview, err
	}
	fingerprint := sha256.Sum256(data)
	preview.Confirmation, preview.Revision = hex.EncodeToString(fingerprint[:]), &revision
	for _, window := range windows {
		preview.Announcements = append(preview.Announcements, maintenanceAnnouncement{
			Title: window.Title, Description: window.Description, Starts: s.clock(window.Starts),
			Ends: s.clock(window.Ends), Status: s.windowStatus(window.Status),
		})
	}
	return preview, nil
}

func statusComponentEffect(state *configdb.State, before, after catalog.StatusPage) statusComponentPreview {
	services := func(page catalog.StatusPage) map[string]bool {
		set := map[string]bool{}
		for _, c := range page.Components {
			for _, name := range state.PageServices(c) {
				set[name] = true
			}
		}
		return set
	}
	was, next := services(before), services(after)
	out := statusComponentPreview{AllNotes: after.AllNotes(), Visible: slices.Sorted(maps.Keys(next))}
	for _, name := range out.Visible {
		if !was[name] {
			out.Added = append(out.Added, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(was)) {
		if !next[name] {
			out.Removed = append(out.Removed, name)
		}
	}
	return out
}

func (s *Server) statusComponentProblem(mode string) string {
	switch mode {
	case "add":
		return s.t("Use a unique component ID, a public title and at least one listed service.")
	case "remove":
		return s.t("Keep at least one component on the public page.")
	default:
		return s.t("Select listed services. Keep at least one explicit service if the component has no selector.")
	}
}

func (s *Server) saveStatusComponent(r *http.Request, v *viewer, page catalog.StatusPage, component string, version int64, revision *uint64) error {
	data, err := json.Marshal(page)
	if err != nil {
		return err
	}
	actor := configdb.Actor{Name: v.name(), Via: "console"}
	note := "Update status page component " + component
	if revision == nil {
		_, err = s.Config.PutIf(r.Context(), actor, configdb.KindStatus, page.Name, data, note, version)
	} else {
		_, err = s.Config.PublishStatusPageIf(r.Context(), actor, page.Name, data, note, version, *revision)
	}
	return err
}

func (s *Server) renderStatusComponent(w http.ResponseWriter, r *http.Request, v *viewer, state *configdb.State,
	page catalog.StatusPage, component catalog.StatusComponent, draft configdb.StatusComponentEdit,
	mode string, preview *statusComponentPreview, problem string, code int,
) {
	title := s.t("Edit services")
	switch mode {
	case "add":
		title = s.t("Add component")
	case "remove":
		title = s.t("Remove component")
	}
	pagePath := consolePath("/status-pages/") + url.PathEscape(page.Name)
	p := statusComponentFormPage{
		shell: s.shellFor(r, v, "status-pages", title,
			crumb{Label: s.t("Status pages"), Href: consolePath("/status-pages")},
			crumb{Label: s.pageTitle(page), Href: pagePath}),
		L: statusComponentLabels{
			Name: s.t("Component ID"), NameHint: s.t("Use lower-case letters, numbers and hyphens."),
			Title: s.t("Public component title"), Services: s.t("Explicit services"),
			ServiceHint: s.t("Select the monitored services this component should include."),
			Automatic:   s.t("Services included by this component's selector"),
			Preview:     s.t("Preview changes"), Publish: s.t("Publish changes"), Edit: s.t("Back"), Cancel: s.t("Cancel"),
			PublicEffect: s.t("Effect on the public page"), Added: s.t("Newly visible services"),
			Removed: s.t("Services that will no longer be visible"), Visible: s.t("Services visible after publishing"),
			NoChange: s.t("The services visible on the public page stay the same."),
			AllNotes: s.t("This page publishes all incident notes for its visible services. Review notes on newly visible services for private information."),
			Remove:   s.t("Remove component"), Destination: s.t("Public status page"), NoServices: s.t("No services"),
			PublicHint:  s.t("Anyone can view this status page. Changes you publish appear there."),
			RemoveHint:  s.t("Preview which services would stay visible after removing this component."),
			Maintenance: s.t("Maintenance announcements that will become public"), Starts: s.t("Starts"), Ends: s.t("Ends"),
		},
		Form: statusComponentPath(page.Name, component.Name, mode), Page: pagePath, PublicURL: s.Status.URL(page, ""),
		ComponentTitle: component.Title.In(string(s.Lang)), Name: draft.Component, Mode: mode, Version: state.Version,
		Preview: preview,
	}
	if mode == "add" {
		p.ComponentTitle = draft.Title
	}
	if mode != "remove" {
		choices := configuredServices(state)
		for _, name := range component.Services {
			if !slices.Contains(choices, name) {
				choices = append(choices, name)
			}
		}
		slices.Sort(choices)
		for _, name := range choices {
			p.Choices = append(p.Choices, option{Value: name, Label: name, Selected: slices.Contains(draft.Services, name)})
		}
	}
	if component.Page != "" && (mode != "remove" || preview == nil) {
		p.Automatic = state.PageServices(catalog.StatusComponent{Page: component.Page})
	}
	p.Problem = problem
	s.renderStatus(w, code, statusComponentFormTmpl, p)
}
