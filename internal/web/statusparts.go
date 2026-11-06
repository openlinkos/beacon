package web

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
)

// The status pages' texts, marks, icons, feed and badges.

// statusLabels are the status pages' own texts, in the page's language.
type statusLabels struct {
	l                                                             i18n.Lang
	Skip, OnThisPage, Language, Components, Maintenance           string
	ActiveIncidents, Ongoing, Scheduled, Past, NoPast, Updates    string
	Operational, Degraded, Down, InMaintenance, NoData, Resolved  string
	Detected, Acknowledged, Update, ByAgent, ByPerson, Affected   string
	Started, Ended, Lasted, Back, Subscribe, JSONLink, Home, Docs string
	Abuse, Privacy, Legend, AllUpdates, Today                     string
	From, Until, Locations                                        string
}

func statusLabelsIn(l i18n.Lang) statusLabels {
	return statusLabels{l: l,
		Skip: i18n.T(l, "Skip to content"), OnThisPage: i18n.T(l, "On this page"), Language: i18n.T(l, "Language"),
		Components: i18n.T(l, "Components"), Maintenance: i18n.T(l, "Maintenance"),
		ActiveIncidents: i18n.T(l, "Active incidents"), Ongoing: i18n.T(l, "Ongoing"), Scheduled: i18n.T(l, "Scheduled"),
		Past: i18n.T(l, "The last 30 days"), NoPast: i18n.T(l, "No incidents in the last 30 days."), Updates: i18n.T(l, "Updates"),
		Operational: i18n.T(l, "Operational"), Degraded: i18n.T(l, "Partial outage"), Down: i18n.T(l, "Outage"),
		InMaintenance: i18n.T(l, "Maintenance"), NoData: i18n.T(l, "No data"), Resolved: i18n.T(l, "Resolved"),
		Detected: i18n.T(l, "Detected"), Acknowledged: i18n.T(l, "Investigating"), Update: i18n.T(l, "Update"),
		ByAgent: i18n.T(l, "Agent"), ByPerson: i18n.T(l, "Person"), Affected: i18n.T(l, "Affected"),
		Started: i18n.T(l, "Started"), Ended: i18n.T(l, "Ended"), Lasted: i18n.T(l, "Lasted"), Back: i18n.T(l, "All components"),
		Subscribe: i18n.T(l, "Atom feed"), JSONLink: "JSON", Home: i18n.T(l, "Home"), Docs: i18n.T(l, "Documentation"),
		Abuse: i18n.T(l, "Report abuse"), Privacy: i18n.T(l, "Privacy"),
		Legend:     i18n.T(l, "Legend"),
		AllUpdates: i18n.T(l, "All updates"), Today: i18n.T(l, "Today"),
		From: i18n.T(l, "From"), Until: i18n.T(l, "Until"), Locations: i18n.T(l, "Probe locations"),
	}
}

func (s statusLabels) percent(v float64) string {
	return i18n.T(s.l, "%s%%", strconv.FormatFloat(v, 'f', -1, 64))
}

func (s statusLabels) cell(date, state string, avail *float64) string {
	if avail == nil {
		return i18n.T(s.l, "%s: %s", date, state)
	}
	return i18n.T(s.l, "%s: %s, %s available", date, state, s.percent(*avail))
}

func (s statusLabels) strip(days int, avail string, down, degraded, maint int) string {
	if avail == "" {
		avail = s.NoData
	}
	return i18n.T(s.l, "The last %d days: %s available; an outage on %d of them, a partial outage on %d, maintenance on %d",
		days, avail, down, degraded, maint)
}

func (s statusLabels) HistoryIn(tz string) string {
	return i18n.T(s.l, "The last 90 days, a day each; days begin at midnight in %s.", tz)
}

func (s statusLabels) updatedAt(t string) string { return i18n.T(s.l, "Updated %s", t) }

func (s statusLabels) startedAt(t string) string { return i18n.T(s.l, "Since %s", t) }

func (s statusLabels) lasted(t, d string) string { return i18n.T(s.l, "%s, for %s", t, d) }

func (s statusLabels) maintenanceOf(title string) string {
	return i18n.T(s.l, "Maintenance: %s", title)
}

func (s statusLabels) Checked(n int) string {
	return i18n.T(s.l, "Checked every minute from %d probe locations; an outage is what at least two of them, and at least half, agree on.", n)
}

// ---- marks and icons

// The brands' marks, as the design system draws them: Beacon's fire and its signal, and the family's
// two linked rings. The hero's version is drawn for the dark panel.
const (
	beaconMark = `<svg viewBox="0 0 48 48" aria-hidden="true"><path d="M14.358 35.491A15 15 0 0 1 14.358 12.509" fill="none" stroke="currentColor" stroke-width="5.5" stroke-linecap="round"/><path d="M33.642 12.509A15 15 0 0 1 33.642 35.491" fill="none" stroke="currentColor" stroke-width="5.5" stroke-linecap="round"/><circle cx="24" cy="24" r="6" class="ol-brand-accent"/></svg>`
	beaconHero = `<svg class="ol-rings-mark" viewBox="0 0 48 48" aria-hidden="true"><path d="M14.358 35.491A15 15 0 0 1 14.358 12.509" fill="none" stroke="currentColor" stroke-width="4" stroke-linecap="round"/><path d="M33.642 12.509A15 15 0 0 1 33.642 35.491" fill="none" stroke="currentColor" stroke-width="4" stroke-linecap="round"/><circle cx="24" cy="24" r="6" class="ol-brand-accent"/></svg>`
	ringsMark  = `<svg viewBox="0 0 48 48" aria-hidden="true"><path d="M26.227 20.144A10 10 0 1 1 20.662 14.695" fill="none" stroke="currentColor" stroke-width="5.5" stroke-linecap="round"/><path class="ol-brand-accent-line" d="M21.773 27.856A10 10 0 1 1 27.338 33.305" fill="none" stroke-width="5.5" stroke-linecap="round"/></svg>`
	ringsHero  = `<svg class="ol-rings-mark" viewBox="0 0 48 48" aria-hidden="true"><path d="M25.912 19.463A10 10 0 1 1 21.349 14.995" fill="none" stroke="currentColor" stroke-width="4" stroke-linecap="round"/><path class="ol-brand-accent-line" d="M22.088 28.537A10 10 0 1 1 26.651 33.005" fill="none" stroke-width="4" stroke-linecap="round"/></svg>`
)

// marks are a brand's mark for the header and for the hero.
func marks(brand string) (template.HTML, template.HTML) {
	if brand == "beacon" {
		return template.HTML(beaconMark), template.HTML(beaconHero) //nolint:gosec // G203: constant markup
	}
	return template.HTML(ringsMark), template.HTML(ringsHero) //nolint:gosec // G203: constant markup
}

// statusIcons are the icons the status pages draw.
var statusIcons = []string{"languages", "chevron-down", "circle-check", "triangle-alert", "circle-x", "wrench", "circle-help",
	"bot", "user", "arrow-left", "menu", "activity", "calendar", "clock", "external-link", "log-in"}

// statusSprite is the hidden <svg> of the status pages' icons, from the vendored set.
var statusSprite = func() template.HTML {
	b, err := static.ReadFile("static/ol/icons.json")
	if err != nil {
		panic(err)
	}
	var set struct {
		ViewBox string            `json:"viewBox"`
		Icons   map[string]string `json:"icons"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		panic(err)
	}
	if !regexp.MustCompile(`^[0-9 ]+$`).MatchString(set.ViewBox) {
		panic("icons.json: an unexpected viewBox")
	}
	var sb strings.Builder
	sb.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" hidden aria-hidden="true">`)
	for _, name := range statusIcons {
		shape, ok := set.Icons[name]
		if !ok || !iconShape.MatchString(shape) {
			panic(fmt.Sprintf("icons.json: icon %q is missing or not plain shapes", name))
		}
		fmt.Fprintf(&sb, `<symbol id="i-%s" viewBox="%s">%s</symbol>`, name, set.ViewBox, shape)
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String()) //nolint:gosec // G203: built from the vendored icon set, checked above to be plain shapes
}()

// ---- the Atom feed

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Lang    string      `xml:"xml:lang,attr"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Author  atomAuthor  `xml:"author"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Links     []atomLink `xml:"link"`
	Summary   string     `xml:"summary"`
	Content   atomText   `xml:"content"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

func atomTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// statusFeed is the page's incidents and maintenance windows as an Atom feed, the newest first.
func (s *Server) statusFeed(w http.ResponseWriter, r *http.Request, name string, onHost bool) {
	p, def, err := s.statusOf(r, name)
	if err != nil {
		statusNotFound(w)
		return
	}
	v := s.statusView(r, p, def, def.Langs()[0], onHost) // a feed is the same for every reader
	updated := time.Time{}
	pageURL := absolute(r, p.URL)
	f := atomFeed{Lang: v.Lang, ID: pageURL, Title: p.Title, Author: atomAuthor{Name: p.Title},
		Links: []atomLink{{Href: pageURL, Rel: "alternate", Type: "text/html"}, {Href: absolute(r, p.Links.Feed), Rel: "self", Type: "application/atom+xml"}}}
	for _, in := range p.Incidents {
		last := in.Started
		var lines []string
		for _, u := range in.Updates { // the newest first
			last = later(last, u.At)
			kind := map[string]string{"detected": v.L.Detected, "acknowledged": v.L.Acknowledged, "resolved": v.L.Resolved}[u.Kind]
			if kind == "" {
				kind = v.L.ByPerson
				if u.By == "agent" {
					kind = v.L.ByAgent
				}
			}
			lines = append(lines, v.clock(u.At)+" · "+kind+": "+u.Text)
		}
		summary := v.stateText(in.Impact)
		if in.Status == "resolved" {
			summary = v.L.Resolved
		}
		f.Entries = append(f.Entries, atomEntry{ID: absolute(r, in.URL), Title: in.Title, Updated: atomTime(last), Published: atomTime(in.Started),
			Links: []atomLink{{Href: absolute(r, in.URL), Rel: "alternate", Type: "text/html"}}, Summary: summary,
			Content: atomText{Type: "text", Text: strings.Join(lines, "\n")}})
		updated = later(updated, last)
	}
	for _, mw := range p.Maintenance {
		text := v.L.From + " " + v.clock(mw.Starts) + " " + v.L.Until + " " + v.clock(mw.Ends)
		if mw.Description != "" {
			text += "\n" + mw.Description
		}
		f.Entries = append(f.Entries, atomEntry{ID: pageURL + "#maintenance-" + mw.ID, Title: v.L.maintenanceOf(mw.Title), Updated: atomTime(mw.Starts),
			Published: atomTime(mw.Starts), Links: []atomLink{{Href: pageURL, Rel: "alternate", Type: "text/html"}},
			Summary: v.componentTitles(mw.Components), Content: atomText{Type: "text", Text: text}})
		updated = later(updated, mw.Starts)
	}
	if updated.IsZero() {
		updated = p.Updated
	}
	f.Updated = atomTime(updated)
	b, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		statusNotFound(w)
		return
	}
	public(w, r, "application/atom+xml; charset=utf-8", append([]byte(xml.Header), append(b, '\n')...), true)
}

// absolute makes a link absolute, as a feed needs, on the host the request came to when Beacon has no public_url.
func absolute(r *http.Request, u string) string {
	if strings.HasPrefix(u, "/") {
		return "https://" + r.Host + u
	}
	return u
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ---- badges

// badgeColours are the design system's solid status colours, and the text on them.
var badgeColours = map[string][2]string{
	status.Operational: {"#15803d", "#ffffff"},
	status.Degraded:    {"#f5a524", "#452a00"},
	status.Down:        {"#dc2626", "#ffffff"},
	status.Maintenance: {"#2563eb", "#ffffff"},
	status.NoData:      {"#8b8d98", "#ffffff"},
}

// textWidth is roughly how wide text is at 11px: wide characters (Chinese) count twice.
func textWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r > 0x2e80:
			w += 11
		case unicode.IsUpper(r):
			w += 8
		default:
			w += 7
		}
	}
	return w
}

// statusBadge is a small SVG with the page's state, or one component's.
func (s *Server) statusBadge(w http.ResponseWriter, r *http.Request, name, component string) {
	p, _, err := s.statusOf(r, name)
	if err != nil {
		statusNotFound(w)
		return
	}
	l := statusLabelsIn(i18n.Lang(p.Language))
	label, state := p.Title, p.Status
	if component != "" {
		c, ok := p.Component(component)
		if !ok {
			statusNotFound(w)
			return
		}
		label, state = c.Title, c.Status
	}
	if utf8.RuneCountInString(label) > 40 {
		label = string([]rune(label)[:39]) + "…"
	}
	v := &statusView{L: l}
	value := v.stateText(state)
	col := badgeColours[state]
	lw, vw := textWidth(label)+12, textWidth(value)+12
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%[1]d" height="20" role="img" aria-label="%[2]s: %[3]s">`+
		`<title>%[2]s: %[3]s</title>`+
		`<clipPath id="r"><rect width="%[1]d" height="20" rx="4"/></clipPath>`+
		`<g clip-path="url(#r)"><rect width="%[4]d" height="20" fill="#3f4049"/><rect x="%[4]d" width="%[5]d" height="20" fill="%[6]s"/></g>`+
		`<g font-family="Geist,Verdana,DejaVu Sans,sans-serif" font-size="11" text-anchor="middle">`+
		`<text x="%[8]d" y="14" fill="#ffffff">%[2]s</text><text x="%[9]d" y="14" fill="%[7]s">%[3]s</text></g></svg>`,
		lw+vw, esc(label), esc(value), lw, vw, col[0], col[1], lw/2, lw+vw/2)
	public(w, r, "image/svg+xml; charset=utf-8", []byte(svg+"\n"), true)
}

// statusFuncs are the status templates' functions: icons from the status pages' own set.
var statusFuncs = template.FuncMap{
	"icon":   func(name string) (template.HTML, error) { return statusIcon(name, "") },
	"iconSm": func(name string) (template.HTML, error) { return statusIcon(name, "sm") },
}

func statusIcon(name, size string) (template.HTML, error) {
	if !slices.Contains(statusIcons, name) {
		return "", fmt.Errorf("no icon %q in the status pages' set", name)
	}
	cls := "ol-icon"
	if size == "sm" {
		cls += " ol-icon-sm"
	}
	return template.HTML(`<svg class="` + cls + `" aria-hidden="true"><use href="#i-` + name + `"/></svg>`), nil //nolint:gosec // G203: a known name and class
}

func (s statusLabels) DaysAgo(n int) string { return i18n.T(s.l, "%d days ago", n) }

func (s statusLabels) Available(pct string) string { return i18n.T(s.l, "%s available", pct) }
