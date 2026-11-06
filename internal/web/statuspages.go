package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The status pages Beacon serves, as the public sees them now, read-only: each page's address, state,
// components, incidents and maintenance windows, with the services behind each component. What a page
// shows is the configuration's business (status_pages, changed through the API, MCP and the command
// line); its incidents are alert groups, its public updates notes that begin with "public:", its
// maintenance planned with schedule_maintenance.

type statusPagesLabels struct {
	Page, Address, State, Components, Incidents, Maintenance, Languages, None, NoPages, Open, JSON, Feed, Badge string
	Component, Group, Services, Availability, Impact, Status, Started, Resolved, Starts, Ends, Title, Problem   string
}

func (s *Server) statusPagesLabels() statusPagesLabels {
	return statusPagesLabels{
		Page: s.t("Status page"), Address: s.t("Address"), State: s.t("State"), Components: s.t("Components"), Incidents: s.t("Incidents"),
		Maintenance: s.t("Maintenance"), Languages: s.t("Languages"), None: s.t("None"),
		NoPages: s.t("No status pages: the configuration's status_pages names none."), Open: s.t("Open the page"), JSON: "JSON",
		Feed: s.t("Feed"), Badge: s.t("Badge"), Component: s.t("Component"), Group: s.t("Group"), Services: s.t("Services"),
		Availability: s.t("Availability (90 days)"), Impact: s.t("Impact"), Status: s.t("Status"), Started: s.t("Started"),
		Resolved: s.t("Resolved"), Starts: s.t("Starts"), Ends: s.t("Ends"), Title: s.t("Title"),
		Problem: s.t("The page could not be computed"),
	}
}

// pageState is a status page's state in the console's words, and its tone.
func (s *Server) pageState(state string) (string, string) {
	switch state {
	case status.Operational:
		return s.t("Operational"), "success"
	case status.Degraded:
		return s.t("Partial outage"), "warning"
	case status.Down:
		return s.t("Outage"), "danger"
	case status.Maintenance:
		return s.t("Maintenance"), "info"
	}
	return s.t("No data"), ""
}

type statusPageRow struct {
	Name, Title, Href, URL, State, Tone, Components, Incidents, Maintenance, Languages string
}

type statusPagesPage struct {
	shell
	L    statusPagesLabels
	Rows []statusPageRow
}

var pageListTmpl = parse("pagelist.html")

func (s *Server) pageList(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := statusPagesPage{shell: s.shellFor(r, v, "status-pages", s.t("Status pages")), L: s.statusPagesLabels()}
	defs := s.Status.Pages()
	p.Count = num(len(defs))
	for _, def := range defs {
		row := statusPageRow{Name: def.Name, Title: s.pageTitle(def), Href: consolePath("/status-pages/") + url.PathEscape(def.Name),
			URL: s.Status.URL(def, ""), Languages: strings.Join(def.Langs(), ", "), Components: num(len(def.Components))}
		pg, err := s.Status.Page(r.Context(), def.Name, string(s.Lang))
		if err != nil {
			row.State = p.L.Problem
		} else {
			row.State, row.Tone = s.pageState(pg.Status)
			active, planned := 0, 0
			for _, in := range pg.Incidents {
				if in.Status == "active" {
					active++
				}
			}
			for _, mw := range pg.Maintenance {
				if mw.Status != "completed" {
					planned++
				}
			}
			row.Incidents, row.Maintenance = count(active), count(planned)
		}
		p.Rows = append(p.Rows, row)
	}
	s.render(w, pageListTmpl, p)
}

// ---- one page

type componentRow struct {
	Name, Title, Group, State, Tone, Availability string
	Services                                      []pageRef
}

type incidentRow struct {
	Title, Href, Impact, ImpactTone, Status, StatusTone, Started, Resolved string
}

type windowRow struct {
	Title, Status, Starts, Ends, Components string
}

type statusPageDetail struct {
	shell
	L                           statusPagesLabels
	URL, Open, JSON, Feed, Logo string
	Components                  []componentRow
	Incidents                   []incidentRow
	Maintenance                 []windowRow
}

var pageDetailTmpl = parse("pagedetail.html")

func (s *Server) pageDetail(w http.ResponseWriter, r *http.Request, v *viewer) {
	def, ok := s.Status.Def(r.PathValue("name"))
	if !ok || r.PathValue("name") == "" {
		s.missing(w, r, v)
		return
	}
	l := s.statusPagesLabels()
	p := statusPageDetail{shell: s.shellFor(r, v, "status-pages", s.pageTitle(def), crumb{Label: s.t("Status pages"), Href: consolePath("/status-pages")}), L: l}
	pg, err := s.Status.Page(r.Context(), def.Name, string(s.Lang))
	if err != nil {
		s.log().Error("console: computing a status page failed", "page", def.Name, "err", err)
		s.message(w, r, v, http.StatusInternalServerError, l.Problem, err.Error())
		return
	}
	state, tn := s.pageState(pg.Status)
	p.Badges = []badge{{Text: state, Tone: tn}}
	p.Count = pg.Summary
	// the page as Beacon serves it on its own host too, so that the console links nowhere else
	p.URL, p.Open = pg.URL, "/status/"+url.PathEscape(def.Name)
	p.JSON, p.Feed, p.Logo = p.Open+".json", p.Open+"/feed.atom", p.Open+"/badge.svg"
	titles := map[string]string{}
	for _, c := range pg.Components {
		row := componentRow{Name: c.Name, Title: c.Title, Group: c.Group}
		row.State, row.Tone = s.pageState(c.Status)
		if c.Availability != nil {
			row.Availability = fmt.Sprintf("%.2f%%", *c.Availability)
		}
		for _, svc := range c.Services() {
			row.Services = append(row.Services, pageRef{Label: svc, Href: consolePath("/monitors/") + url.PathEscape(svc)})
		}
		titles[c.Name] = c.Title
		p.Components = append(p.Components, row)
	}
	for _, in := range pg.Incidents {
		row := incidentRow{Title: in.Title, Started: s.clock(in.Started), Status: s.t("Ongoing"), StatusTone: "danger"}
		row.Impact, row.ImpactTone = s.pageState(in.Impact)
		if in.Status == "resolved" {
			row.Status, row.StatusTone = s.t("Resolved"), "success"
		}
		if in.Resolved != nil {
			row.Resolved = s.clock(*in.Resolved)
		}
		if _, ok := s.Store.Group(in.ID); ok {
			row.Href = link.AlertPath(in.ID)
		}
		p.Incidents = append(p.Incidents, row)
	}
	for _, mw := range pg.Maintenance {
		var comps []string
		for _, c := range mw.Components {
			comps = append(comps, titles[c])
		}
		p.Maintenance = append(p.Maintenance, windowRow{Title: mw.Title, Status: s.windowStatus(mw.Status), Starts: s.clock(mw.Starts), Ends: s.clock(mw.Ends),
			Components: strings.Join(comps, ", ")})
	}
	s.render(w, pageDetailTmpl, p)
}

// windowStatus is a maintenance window's status in words.
func (s *Server) windowStatus(st string) string {
	switch st {
	case "active":
		return s.t("In progress")
	case "completed":
		return s.t("Completed")
	}
	return s.t("Scheduled")
}
