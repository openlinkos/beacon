package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
)

// The public status pages (package status computes them): served beside the console without signing
// in, at / and /status (the default page) and /status/<name>, with each incident's page, the page's
// JSON, an Atom feed and badges; and at / on a page's own host name, where nothing else of Beacon is
// reachable. They are on the family's site design (the site header with the family menu and the way
// into the console, a hero with the overall state, sections, the shared footer with the line about
// Beacon), need no script, and are public and cacheable: a short Cache-Control and an ETag, indexed by
// search engines (no X-Robots-Tag; robots.txt allows them), the console's Content-Security-Policy
// without its script.

//go:embed templates/status/*.html
var statusTemplates embed.FS

func parseStatus(page string) *template.Template {
	return template.Must(template.New("").Funcs(statusFuncs).ParseFS(statusTemplates, "templates/status/layout.html", "templates/status/"+page))
}

var (
	statusPageTmpl     = parseStatus("page.html")
	statusIncidentTmpl = parseStatus("incident.html")
)

// statusCSP is the status pages' Content-Security-Policy: styles, fonts and images from Beacon only, no
// script at all, no forms.
const statusCSP = "default-src 'none'; style-src 'self'; font-src 'self'; img-src 'self' data:; base-uri 'none'; " +
	"form-action 'none'; frame-ancestors 'none'"

// statusMaxAge is how long browsers and caches may keep a status page, its JSON, feed and badges.
// A page for someone signed in to the console, whose header says Console rather than Sign in, is
// privateMaxAge: no shared cache keeps it. (A shared cache may still hand a signed-in reader the page
// that says Sign in: both lead to /console, which knows who is signed in.)
const (
	statusMaxAge  = "public, max-age=30"
	privateMaxAge = "private, max-age=30"
)

func (s *Server) registerStatus(mux *http.ServeMux) {
	mux.HandleFunc("GET /robots.txt", s.robots)
	if s.Status == nil {
		return
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { s.statusPage(w, r, "", false) })
	mux.HandleFunc("GET /status.json", func(w http.ResponseWriter, r *http.Request) { s.statusJSON(w, r, "") })
	mux.HandleFunc("GET /status/{file}", func(w http.ResponseWriter, r *http.Request) {
		if name, ok := strings.CutSuffix(r.PathValue("file"), ".json"); ok {
			s.statusJSON(w, r, name)
			return
		}
		s.statusPage(w, r, r.PathValue("file"), false)
	})
	mux.HandleFunc("GET /status/{name}/feed.atom", func(w http.ResponseWriter, r *http.Request) { s.statusFeed(w, r, r.PathValue("name"), false) })
	mux.HandleFunc("GET /status/{name}/badge.svg", func(w http.ResponseWriter, r *http.Request) { s.statusBadge(w, r, r.PathValue("name"), "") })
	mux.HandleFunc("GET /status/{name}/components/{component}/badge.svg", func(w http.ResponseWriter, r *http.Request) {
		s.statusBadge(w, r, r.PathValue("name"), r.PathValue("component"))
	})
	mux.HandleFunc("GET /status/{name}/incidents/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.statusIncident(w, r, r.PathValue("name"), r.PathValue("id"), false)
	})
}

// Hosts serves a status page's own host name: the page at /, its incidents under /incidents/, its
// JSON, feed and badges, robots.txt and the static files the page needs; nothing else of Beacon, the
// console, the API and MCP included, answers there. Other hosts go to next.
func (s *Server) Hosts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil || s.Status == nil {
			next.ServeHTTP(w, r)
			return
		}
		def, ok := s.Status.ByHost(hostOnly(r.Host))
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			statusHeaders(w)
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		switch {
		case p == "/":
			s.statusPage(w, r, def.Name, true)
		case p == "/status.json":
			s.statusJSON(w, r, def.Name)
		case p == "/feed.atom":
			s.statusFeed(w, r, def.Name, true)
		case p == "/badge.svg":
			s.statusBadge(w, r, def.Name, "")
		case p == "/robots.txt":
			statusHeaders(w)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
		case strings.HasPrefix(p, "/static/"):
			s.static(w, r)
		case strings.HasPrefix(p, "/incidents/") && !strings.Contains(p[len("/incidents/"):], "/"):
			s.statusIncident(w, r, def.Name, p[len("/incidents/"):], true)
		case strings.HasPrefix(p, "/components/") && strings.HasSuffix(p, "/badge.svg"):
			c := strings.TrimSuffix(strings.TrimPrefix(p, "/components/"), "/badge.svg")
			if strings.Contains(c, "/") {
				statusNotFound(w)
				return
			}
			s.statusBadge(w, r, def.Name, c)
		default:
			statusNotFound(w)
		}
	})
}

// hostOnly is the host name of a Host header, without the port, in lower case.
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// robotsTxt lets search engines read the public pages (/, the status pages and what they load) and
// keeps them out of the console, the API, MCP and the old paths of the pages that signed links open.
const robotsTxt = "User-agent: *\nAllow: /\nAllow: /status\nAllow: /static/\n" +
	"Disallow: /console\nDisallow: /api/\nDisallow: /mcp\nDisallow: /alerts/\nDisallow: /sessions/\n"

func (s *Server) robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(robotsTxt))
}

// statusHeaders are set on every answer of the status pages: no X-Robots-Tag, unlike the console's.
func statusHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", statusCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func statusNotFound(w http.ResponseWriter) {
	statusHeaders(w)
	w.Header().Set("Cache-Control", "public, max-age=30")
	http.Error(w, "404 page not found", http.StatusNotFound)
}

// public writes a public answer with its entity tag, or 304 when the client has it already. Open is
// for what other sites may embed or fetch (the JSON, the feed, the badges).
func public(w http.ResponseWriter, r *http.Request, contentType string, body []byte, open bool) {
	cached(w, r, contentType, body, open, statusMaxAge)
}

// cached is public with the Cache-Control given.
func cached(w http.ResponseWriter, r *http.Request, contentType string, body []byte, open bool, cacheControl string) {
	statusHeaders(w)
	h := w.Header()
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	h.Set("ETag", etag)
	h.Set("Cache-Control", cacheControl)
	if open {
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	}
	if match(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", contentType)
	_, _ = w.Write(body)
}

// match reports whether an If-None-Match header names the entity tag.
func match(header, etag string) bool {
	for t := range strings.SplitSeq(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == etag || t == "*" {
			return true
		}
	}
	return false
}

// statusOf computes the page called name ("": the default) in the language the request asks for.
func (s *Server) statusOf(r *http.Request, name string) (*status.Page, catalog.StatusPage, error) {
	def, ok := s.Status.Def(name)
	if !ok {
		return nil, def, status.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p, err := s.Status.Page(ctx, def.Name, r.URL.Query().Get("lang"))
	return p, def, err
}

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request, name string, onHost bool) {
	p, def, err := s.statusOf(r, name)
	if err != nil {
		statusNotFound(w)
		return
	}
	s.renderPublic(w, r, statusPageTmpl, s.statusView(r, p, def, onHost))
}

func (s *Server) statusIncident(w http.ResponseWriter, r *http.Request, name, id string, onHost bool) {
	p, def, err := s.statusOf(r, name)
	if err != nil {
		statusNotFound(w)
		return
	}
	in, ok := p.Incident(id)
	if !ok {
		statusNotFound(w)
		return
	}
	v := s.statusView(r, p, def, onHost)
	iv := v.incident(in, true)
	v.Incident = &iv
	v.Title = in.Title + " – " + p.Title
	s.renderPublic(w, r, statusIncidentTmpl, v)
}

// renderPublic writes a public page: cacheable by anyone, unless it names the reader's sign-in.
func (s *Server) renderPublic(w http.ResponseWriter, r *http.Request, t *template.Template, v *statusView) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.log().Error("status page template failed", "err", err)
		statusHeaders(w)
		http.Error(w, "500 internal server error", http.StatusInternalServerError)
		return
	}
	cacheControl := statusMaxAge
	if v.private {
		cacheControl = privateMaxAge
	}
	cached(w, r, "text/html; charset=utf-8", buf.Bytes(), false, cacheControl)
}

func (s *Server) statusJSON(w http.ResponseWriter, r *http.Request, name string) {
	p, _, err := s.statusOf(r, name)
	if err != nil {
		statusNotFound(w)
		return
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		statusNotFound(w)
		return
	}
	public(w, r, "application/json; charset=utf-8", append(b, '\n'), true)
}

// ---- the view

// statusView is what the templates of the public pages read: a status page, one of its incidents, or
// the page at / without a status page (P nil). Name is the site's, in the header and the footer;
// SignIn the header's way into the console (nil where there is none); Powered the footer's line about
// Beacon (nil when the configuration leaves it out).
type statusView struct {
	Lang, Title, Name, Brand, Version string
	Mark, MarkHero, Icons             template.HTML
	SignIn                            *linkView
	Powered                           *powered
	P                                 *status.Page
	L                                 statusLabels
	Root, Canonical, Feed, JSON       string
	Description                       string
	Tone, StateText                   string
	Langs                             []langLink
	LangName                          string
	Alternates                        []langLink
	Menu                              *menuView
	FooterLinks                       []linkView
	Updated                           string
	Active                            []incidentView
	Ongoing, Upcoming                 []windowView
	Groups                            []componentGroup
	Past                              []pastItem
	Incident                          *incidentView
	Locations                         int

	viewState
	private bool // the page names the reader's sign-in
}

type langLink struct {
	Code, Name, Href, HrefLang string
	Current                    bool
}

type linkView struct{ Title, URL string }

type menuView struct {
	Title  string
	Groups []menuGroup
}

type menuGroup struct {
	Title string
	Links []linkView
}

type componentGroup struct {
	Title, Description string
	Items              []componentView
}

type componentView struct {
	Name, Title, Description string
	State, Tone, Avail       string
	Strip                    string
	Cells                    []cellView
}

type cellView struct{ Class, Title string }

type incidentView struct {
	ID, Title, Href, Tone, Impact, State string
	ImpactTone                           string
	Components                           string
	Started, StartedISO                  string
	Resolved, ResolvedISO, Took          string
	Updates                              []updateView
}

type updateView struct {
	When, WhenISO, Kind, Text, By string
	Agent                         bool
}

type windowView struct {
	Title, Description, Components   string
	Starts, StartsISO, Ends, EndsISO string
}

type pastItem struct {
	Day, Title, Href, Tone, Meta string
}

// statusView builds the view of a page for a request: on its own host, or under /status.
func (s *Server) statusView(r *http.Request, p *status.Page, def catalog.StatusPage, onHost bool) *statusView {
	l := i18n.Lang(p.Language)
	base, root := "/status/"+def.Name, "/status/"+def.Name
	jsonPath := base + ".json"
	if onHost {
		base, root, jsonPath = "", "/", "/status.json"
	}
	q := ""
	if p.Language != def.Langs()[0] {
		q = "?lang=" + url.QueryEscape(p.Language)
	}
	brand := def.Brand
	if brand == "" {
		brand = "beacon"
	}
	v := &statusView{Lang: l.HTML(), Title: p.Title, Name: p.Title, Brand: brand, Version: s.version(), Icons: statusSprite, P: p, L: statusLabelsIn(l),
		Root: root + q, Canonical: p.URL, Feed: base + "/feed.atom", JSON: jsonPath, Description: p.Description, Locations: len(p.Locations)}
	v.Mark, v.MarkHero = marks(brand)
	v.SignIn, v.private = s.consoleLink(r, l, onHost)
	v.Powered = s.poweredBy(l)
	v.base, v.q, v.l, v.loc = base, q, l, s.loc()
	v.Tone, v.StateText = stateTone(p.Status), v.stateText(p.Status)
	v.Updated = v.L.updatedAt(v.clock(p.Updated))
	for _, code := range def.Langs() { // the same page in each language
		lg := i18n.Lang(code)
		href := r.URL.Path
		if code != def.Langs()[0] {
			href += "?lang=" + code
		}
		ll := langLink{Code: lg.HTML(), Name: lg.Name(), Href: href, HrefLang: lg.HTML(), Current: code == p.Language}
		v.Langs = append(v.Langs, ll)
		if ll.Current {
			v.LangName = ll.Name
		}
	}
	if len(v.Langs) < 2 {
		v.Langs = nil
	}
	if len(def.Menu.Groups) > 0 {
		m := &menuView{Title: def.Menu.Title.In(p.Language)}
		for _, g := range def.Menu.Groups {
			mg := menuGroup{Title: g.Title.In(p.Language)}
			for _, ln := range g.Links {
				mg.Links = append(mg.Links, linkView{Title: ln.Title.In(p.Language), URL: ln.URL})
			}
			m.Groups = append(m.Groups, mg)
		}
		v.Menu = m
	}
	for _, ln := range []linkView{{v.L.Home, p.Links.Home}, {v.L.Docs, p.Links.Docs}, {v.L.Abuse, p.Links.Abuse}, {v.L.Privacy, p.Links.Privacy}} {
		if ln.URL != "" {
			v.FooterLinks = append(v.FooterLinks, ln)
		}
	}
	for _, in := range p.Incidents {
		if in.Status == "active" {
			v.Active = append(v.Active, v.incident(in, false))
		}
	}
	for _, w := range p.Maintenance {
		wv := v.window(w)
		switch w.Status {
		case "active":
			v.Ongoing = append(v.Ongoing, wv)
		case "scheduled":
			v.Upcoming = append(v.Upcoming, wv)
		}
	}
	v.Groups = v.componentGroups(p)
	v.Past = v.past(p)
	return v
}

// The view's own state, for its helpers.
type viewState struct {
	base, q string
	l       i18n.Lang
	loc     *time.Location
}

func (v *statusView) clock(t time.Time) string { return t.In(v.loc).Format("2006-01-02 15:04 MST") }

func (v *statusView) stateText(state string) string {
	switch state {
	case status.Operational:
		return v.L.Operational
	case status.Degraded:
		return v.L.Degraded
	case status.Down:
		return v.L.Down
	case status.Maintenance:
		return v.L.InMaintenance
	}
	return v.L.NoData
}

// stateTone is the design system's tone of a state.
func stateTone(state string) string {
	switch state {
	case status.Operational:
		return "success"
	case status.Degraded:
		return "warning"
	case status.Down:
		return "danger"
	case status.Maintenance:
		return "info"
	}
	return ""
}

func (v *statusView) incident(in status.Incident, all bool) incidentView {
	iv := incidentView{ID: in.ID, Title: in.Title, Href: v.base + "/incidents/" + in.ID + v.q, Tone: stateTone(in.Impact), ImpactTone: stateTone(in.Impact), Impact: v.stateText(in.Impact),
		Started: v.clock(in.Started), StartedISO: in.Started.UTC().Format(time.RFC3339)}
	iv.State = v.L.Ongoing
	if in.Status == "resolved" {
		iv.State, iv.Tone = v.L.Resolved, "success"
	}
	iv.Components = v.componentTitles(in.Components)
	if in.Resolved != nil {
		iv.Resolved, iv.ResolvedISO = v.clock(*in.Resolved), in.Resolved.UTC().Format(time.RFC3339)
		iv.Took = roughly(v.l, in.Resolved.Sub(in.Started))
	}
	for i, u := range in.Updates {
		if !all && i >= 3 {
			break
		}
		uv := updateView{When: v.clock(u.At), WhenISO: u.At.UTC().Format(time.RFC3339), Text: u.Text, Agent: u.By == "agent"}
		switch u.Kind {
		case "detected":
			uv.Kind = v.L.Detected
		case "acknowledged":
			uv.Kind = v.L.Acknowledged
		case "resolved":
			uv.Kind = v.L.Resolved
		default:
			uv.Kind = v.L.Update
			uv.By = v.L.ByPerson
			if uv.Agent {
				uv.By = v.L.ByAgent
			}
		}
		iv.Updates = append(iv.Updates, uv)
	}
	return iv
}

func (v *statusView) componentTitles(names []string) string {
	var out []string
	for _, n := range names {
		if c, ok := v.P.Component(n); ok {
			out = append(out, c.Title)
		}
	}
	return joinList(v.l, out)
}

// joinList joins items as the language does.
func joinList(l i18n.Lang, items []string) string {
	out := ""
	for i, it := range items {
		if i == 0 {
			out = it
		} else {
			out = i18n.T(l, "%s, %s", out, it)
		}
	}
	return out
}

func (v *statusView) window(w status.Window) windowView {
	return windowView{Title: w.Title, Description: w.Description, Components: v.componentTitles(w.Components),
		Starts: v.clock(w.Starts), StartsISO: w.Starts.UTC().Format(time.RFC3339), Ends: v.clock(w.Ends), EndsISO: w.Ends.UTC().Format(time.RFC3339)}
}

func (v *statusView) componentGroups(p *status.Page) []componentGroup {
	view := func(c status.Component) componentView {
		cv := componentView{Name: c.Name, Title: c.Title, Description: c.Description, State: v.stateText(c.Status), Tone: stateTone(c.Status)}
		if c.Availability != nil {
			cv.Avail = v.L.percent(*c.Availability)
		}
		var down, degraded, maint int
		for _, d := range c.History {
			cell := cellView{Title: v.L.cell(d.Date, v.stateText(d.Status), d.Availability)}
			switch d.Status {
			case status.Operational:
				cell.Class = "ol-uptime-ok"
			case status.Degraded:
				cell.Class, degraded = "ol-uptime-degraded", degraded+1
			case status.Down:
				cell.Class, down = "ol-uptime-down", down+1
			case status.Maintenance:
				cell.Class, maint = "bc-uptime-maintenance", maint+1
			}
			cv.Cells = append(cv.Cells, cell)
		}
		cv.Strip = v.L.strip(len(c.History), cv.Avail, down, degraded, maint)
		return cv
	}
	// the components in no group first, without a heading, then each group under its own
	var rest componentGroup
	for _, c := range p.Components {
		if c.Group == "" {
			rest.Items = append(rest.Items, view(c))
		}
	}
	var out []componentGroup
	if len(rest.Items) > 0 {
		out = append(out, rest)
	}
	for _, g := range p.Groups {
		cg := componentGroup{Title: g.Title, Description: g.Description}
		for _, name := range g.Components {
			if c, ok := p.Component(name); ok {
				cg.Items = append(cg.Items, view(c))
			}
		}
		out = append(out, cg)
	}
	return out
}

// past lists what ended in the last 30 days, incidents and maintenance, the newest first.
func (v *statusView) past(p *status.Page) []pastItem {
	type item struct {
		at time.Time
		pastItem
	}
	var items []item
	for _, in := range p.Incidents {
		if in.Status != "resolved" {
			continue
		}
		meta := v.L.startedAt(v.clock(in.Started))
		if in.Resolved != nil {
			meta = v.L.lasted(v.clock(in.Started), roughly(v.l, in.Resolved.Sub(in.Started)))
		}
		items = append(items, item{in.Started, pastItem{Title: in.Title, Href: v.base + "/incidents/" + in.ID + v.q, Tone: stateTone(in.Impact), Meta: meta}})
	}
	for _, w := range p.Maintenance {
		if w.Status == "completed" {
			items = append(items, item{w.Starts, pastItem{Title: v.L.maintenanceOf(w.Title), Tone: "info",
				Meta: v.L.lasted(v.clock(w.Starts), roughly(v.l, w.Ends.Sub(w.Starts)))}})
		}
	}
	for i := 1; i < len(items); i++ { // newest first; few items
		for j := i; j > 0 && items[j].at.After(items[j-1].at); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	out := make([]pastItem, 0, len(items))
	for _, it := range items {
		it.Day = it.at.In(v.loc).Format(time.DateOnly)
		out = append(out, it.pastItem)
	}
	return out
}

// roughly renders a duration the way people say it, in l.
func roughly(l i18n.Lang, d time.Duration) string {
	switch {
	case d < time.Minute:
		return i18n.T(l, "under a minute")
	case d < time.Hour:
		return i18n.T(l, "%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return i18n.T(l, "%d h", h)
		}
		return i18n.T(l, "%d h %d min", h, m)
	}
	return i18n.T(l, "%d d %d h", int(d.Hours())/24, int(d.Hours())%24)
}
