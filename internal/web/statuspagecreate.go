package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

const statusPageCreatePath = "/status-pages/new/"

var statusPageCreateTmpl = parse("statuspagecreate.html")

type statusPageCreateLabels struct {
	Name, NameHint, Title, Services, ServiceHint, NoServices, Monitors string
	Preview, Publish, Edit, Cancel, PublicEffect, Destination          string
	PublicNotes, DefaultAddress                                        string
}

type statusPageCreatePage struct {
	shell             `json:"-"`
	L                 statusPageCreateLabels
	Name, PublicTitle string
	Form, Back        string
	Monitors, URL     string
	DefaultAddress    bool
	StateVersion      int64
	Services          []option
	Preview           *statusComponentPreview
}

func (s *Server) statusPageDefaultHelp() string {
	return s.t("Mark the current page as the default in the configuration before creating another.")
}

func (s *Server) createStatusPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	if s.Config == nil || s.Status == nil {
		s.missing(w, r, v)
		return
	}
	state := s.Config.State()
	if !state.CanCreateStatusPage() {
		s.message(w, r, v, http.StatusConflict, s.t("Create status page"), s.statusPageDefaultHelp())
		return
	}
	lang, ok := i18n.Parse(string(s.Lang))
	if !ok {
		lang = i18n.EN
	}
	in := configdb.StatusPageDraft{Language: string(lang), ComponentTitle: i18n.T(lang, "Monitored services")}
	var draft catalog.StatusPage
	show := func(preview *statusComponentPreview, problem string, status int) {
		s.renderStatusPageCreate(w, r, v, state, in, draft, preview, problem, status)
	}
	if r.Method != http.MethodPost {
		show(nil, "", http.StatusOK)
		return
	}
	in.Name, in.Title = strings.TrimSpace(r.PostForm.Get("name")), strings.TrimSpace(r.PostForm.Get("title"))
	in.Services = r.PostForm["service"]
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil || version < 1 || version != state.Version {
		show(nil, s.t("The status page configuration changed. Review the current services and preview again."), http.StatusConflict)
		return
	}
	intent := r.PostForm.Get("intent")
	if intent == "edit" {
		show(nil, "", http.StatusOK)
		return
	}
	draft, err = state.DraftStatusPage(in)
	switch {
	case errors.Is(err, configdb.ErrStatusPageExists):
		show(nil, s.t("A status page already uses that ID."), http.StatusConflict)
		return
	case err != nil:
		show(nil, s.t("Enter a valid page ID and public title, and select at least one listed service."), http.StatusBadRequest)
		return
	case intent != "preview" && intent != "publish":
		show(nil, s.t("Preview the change or publish it."), http.StatusBadRequest)
		return
	}
	check := func() (*statusComponentPreview, bool) {
		p, err := s.statusComponentPreview(state, catalog.StatusPage{}, draft, in.Title)
		if err != nil || p.Revision == nil {
			s.log().Error("console: reading the first status page's publication preview", "err", err)
			show(nil, s.t("The public preview could not be prepared. Try again."), http.StatusServiceUnavailable)
			return nil, false
		}
		return &p, true
	}
	preview, ok := check()
	if !ok {
		return
	}
	if intent == "preview" {
		show(preview, "", http.StatusOK)
		return
	}
	confirmation := r.PostForm.Get("confirmation")
	if confirmation == "" || confirmation != preview.Confirmation {
		show(preview, s.t("Review the public content below before publishing."), http.StatusConflict)
		return
	}
	raw, err := json.Marshal(draft)
	if err == nil {
		_, err = s.Config.PublishStatusPageIf(r.Context(), configdb.Actor{Name: v.name(), Via: "console"},
			draft.Name, raw, "Create status page "+draft.Name, version, *preview.Revision)
	}
	switch {
	case errors.Is(err, store.ErrMaintenanceChanged):
		if refreshed, ok := check(); ok {
			show(refreshed, s.t("Review the public content below before publishing."), http.StatusConflict)
		}
	case errors.Is(err, configdb.ErrConflict):
		s.message(w, r, v, http.StatusConflict, s.t("Configuration changed"), s.t("Open the status page list and preview the new page again."))
	case err != nil:
		s.failed(w, r, consolePath(statusPageCreatePath), "create status page", err)
	default:
		back(w, r, consolePath("/status-pages/")+url.PathEscape(draft.Name), "done", "status_page_created")
	}
}

func (s *Server) renderStatusPageCreate(w http.ResponseWriter, r *http.Request, v *viewer, state *configdb.State,
	in configdb.StatusPageDraft, draft catalog.StatusPage, preview *statusComponentPreview, problem string, code int,
) {
	p := statusPageCreatePage{
		shell: s.shellFor(r, v, "status-pages", s.t("Create status page"),
			crumb{Label: s.t("Status pages"), Href: consolePath("/status-pages")}),
		L: statusPageCreateLabels{
			Name: s.t("Page ID"), NameHint: s.t("The ID is part of the public address. Use lower-case letters, numbers and hyphens."),
			Title: s.t("Public page title"), Services: s.t("Services"),
			ServiceHint: s.t("Choose the monitored services to include in the first component."),
			NoServices:  s.t("Add an HTTP monitor before creating a status page."), Monitors: s.t("View monitors"),
			Preview: s.t("Preview changes"), Publish: s.t("Publish status page"), Edit: s.t("Back"), Cancel: s.t("Cancel"),
			PublicEffect: s.t("Effect on the public page"), Destination: s.t("Public status page"),
			PublicNotes:    s.t("Only incident notes marked public appear on this page."),
			DefaultAddress: s.t("This first status page will also open at / and /status."),
		},
		Name: in.Name, PublicTitle: in.Title, StateVersion: state.Version, Preview: preview,
		Form: consolePath(statusPageCreatePath), Back: consolePath("/status-pages"), Monitors: consolePath("/monitors"),
	}
	for _, service := range configuredServices(state) {
		p.Services = append(p.Services, option{Value: service, Label: service, Selected: slices.Contains(in.Services, service)})
	}
	if preview != nil {
		p.URL = s.Status.URL(draft, "")
		p.DefaultAddress = draft.Default
	}
	p.Problem = problem
	s.renderStatus(w, code, statusPageCreateTmpl, p)
}
