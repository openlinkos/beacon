package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// A status page is what Beacon shows the public about some of the services it monitors: their state
// now, 90 days of history, incidents and maintenance (package status computes it, package web serves
// it). Its definition lives in the catalogue, under status_pages, so that it is changed, versioned and
// imported like the rest: what a page shows is chosen here; what it says comes from the probes, the
// alert groups and the maintenance windows.

// StatusPage is one status page. Name is its id in URLs (/status/<name>); Host, when set, is a host
// name of its own, where it is served at /. Components are what it lists, each backed by the probe
// targets of some services; Groups, if any, put components under headings. Languages are those it is
// shown in (en, zh), the first one the default. Alerts are the alert names that count as incidents (the
// availability alerts by default); Notes says which of their notes are published: public (those that
// begin with "public:") or all.
type StatusPage struct {
	Name        string            `yaml:"name" json:"name"`
	Title       Text              `yaml:"title" json:"title"`
	Description Text              `yaml:"description,omitempty" json:"description,omitempty"`
	Host        string            `yaml:"host,omitempty" json:"host,omitempty"`
	Default     bool              `yaml:"default,omitempty" json:"default,omitempty"`
	Brand       string            `yaml:"brand,omitempty" json:"brand,omitempty"`
	Languages   []string          `yaml:"languages,omitempty" json:"languages,omitempty"`
	Components  []StatusComponent `yaml:"components" json:"components"`
	Groups      []StatusGroup     `yaml:"groups,omitempty" json:"groups,omitempty"`
	Alerts      []string          `yaml:"alerts,omitempty" json:"alerts,omitempty"`
	Notes       string            `yaml:"notes,omitempty" json:"notes,omitempty"`
	Links       StatusLinks       `yaml:"links,omitempty" json:"links,omitzero"`
	Menu        StatusMenu        `yaml:"menu,omitempty" json:"menu,omitzero"`
}

// StatusComponent is one line of a status page: a title and a description for the public, and the
// services whose probes say how it does, named by their service label (Services) or by a page label on
// their targets (Page: every service with a target labelled page: <Page>).
type StatusComponent struct {
	Name        string   `yaml:"name" json:"name"`
	Title       Text     `yaml:"title" json:"title"`
	Description Text     `yaml:"description,omitempty" json:"description,omitempty"`
	Services    []string `yaml:"services,omitempty" json:"services,omitempty"`
	Page        string   `yaml:"page,omitempty" json:"page,omitempty"`
}

// StatusGroup puts components under a heading.
type StatusGroup struct {
	Title       Text     `yaml:"title" json:"title"`
	Description Text     `yaml:"description,omitempty" json:"description,omitempty"`
	Components  []string `yaml:"components" json:"components"`
}

// StatusLinks are the links a page shows in its footer: home, documentation, where to report abuse,
// the privacy notice.
type StatusLinks struct {
	Home    string `yaml:"home,omitempty" json:"home,omitempty"`
	Docs    string `yaml:"docs,omitempty" json:"docs,omitempty"`
	Abuse   string `yaml:"abuse,omitempty" json:"abuse,omitempty"`
	Privacy string `yaml:"privacy,omitempty" json:"privacy,omitempty"`
}

// StatusMenu is the family menu in the page's header, and the columns of its footer: a button (Title)
// that opens groups of links, such as the organisation's home, its open-source projects and its free
// public services. A group without a title is a plain list of links.
type StatusMenu struct {
	Title  Text              `yaml:"title,omitempty" json:"title,omitempty"`
	Groups []StatusMenuGroup `yaml:"groups,omitempty" json:"groups,omitempty"`
}

// StatusMenuGroup is one group of the family menu.
type StatusMenuGroup struct {
	Title Text         `yaml:"title,omitempty" json:"title,omitempty"`
	Links []StatusLink `yaml:"links" json:"links"`
}

// StatusLink is a link in the family menu.
type StatusLink struct {
	Title Text   `yaml:"title" json:"title"`
	URL   string `yaml:"url" json:"url"`
}

// StatusLanguages are the languages a status page may be shown in.
var StatusLanguages = []string{"en", "zh"}

// StatusBrands are the design system's brands a status page may wear (its accent colour and mark);
// beacon when it names none.
var StatusBrands = []string{"beacon", "linkos", "linkos-status", "linkos-mirrors", "openlinkos"}

// DefaultStatusAlerts are the alerts that are incidents on a page that names none: the availability
// alerts of the reference rules, which the probes' agreement raises.
var DefaultStatusAlerts = []string{"ServiceDown", "ServiceDegraded", "MainlandPathDegraded"}

// Langs are the page's languages, the default first.
func (p StatusPage) Langs() []string {
	if len(p.Languages) == 0 {
		return []string{"en"}
	}
	return p.Languages
}

// IncidentAlerts are the alert names that are incidents on the page.
func (p StatusPage) IncidentAlerts() []string {
	if len(p.Alerts) == 0 {
		return DefaultStatusAlerts
	}
	return p.Alerts
}

// AllNotes reports whether every note on an incident is published, not only those marked public.
func (p StatusPage) AllNotes() bool { return p.Notes == "all" }

// Component returns the page's component called name.
func (p StatusPage) Component(name string) (StatusComponent, bool) {
	for _, c := range p.Components {
		if c.Name == name {
			return c, true
		}
	}
	return StatusComponent{}, false
}

// StatusPage returns the status page called name.
func (c *Catalog) StatusPage(name string) (StatusPage, bool) {
	for _, p := range c.StatusPages {
		if p.Name == name {
			return p, true
		}
	}
	return StatusPage{}, false
}

// DefaultStatusPage is the page served at /status: the one marked default, else the first by name.
func (c *Catalog) DefaultStatusPage() (StatusPage, bool) {
	if len(c.StatusPages) == 0 {
		return StatusPage{}, false
	}
	for _, p := range c.StatusPages {
		if p.Default {
			return p, true
		}
	}
	ps := slices.Clone(c.StatusPages)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	return ps[0], true
}

// StatusPageByHost returns the status page served at the host name host (without a port).
func (c *Catalog) StatusPageByHost(host string) (StatusPage, bool) {
	host = strings.ToLower(host)
	for _, p := range c.StatusPages {
		if p.Host != "" && p.Host == host {
			return p, true
		}
	}
	return StatusPage{}, false
}

// ---- texts in one language or several

// Text is what a page says to people: the same in every language (a plain string) or one per language
// ({en: …, zh: …}).
type Text map[string]string

// In is the text in lang: its own, or the one for every language, or English, or any.
func (t Text) In(lang string) string {
	if s, ok := t[lang]; ok && s != "" {
		return s
	}
	if s := t[""]; s != "" {
		return s
	}
	if s := t["en"]; s != "" {
		return s
	}
	for _, k := range sortedTextKeys(t) {
		if t[k] != "" {
			return t[k]
		}
	}
	return ""
}

// Empty reports whether the text says nothing in any language.
func (t Text) Empty() bool { return t.In("") == "" }

func sortedTextKeys(t Text) []string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// plain reports whether the text is one string for every language.
func (t Text) plain() bool { _, ok := t[""]; return len(t) == 1 && ok }

// MarshalJSON writes a plain text as a string, one per language as an object.
func (t Text) MarshalJSON() ([]byte, error) {
	if t.plain() {
		return json.Marshal(t[""])
	}
	return json.Marshal(map[string]string(t))
}

// UnmarshalJSON reads a string or an object of languages.
func (t *Text) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = Text{"": s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return errors.New("a text is a string, or an object with one per language such as {\"en\": \"…\", \"zh\": \"…\"}")
	}
	*t = m
	return nil
}

// MarshalYAML writes a plain text as a string, one per language as a mapping.
func (t Text) MarshalYAML() (any, error) {
	if t.plain() {
		return t[""], nil
	}
	return map[string]string(t), nil
}

// UnmarshalYAML reads a string or a mapping of languages.
func (t *Text) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*t = Text{"": s}
		return nil
	}
	var m map[string]string
	if err := n.Decode(&m); err != nil {
		return fmt.Errorf("line %d: a text is a string, or a mapping with one per language such as {en: …, zh: …}", n.Line)
	}
	*t = m
	return nil
}

// ---- checks

var (
	hostRe      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	alertNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,99}$`)
)

// labelValueOK reports whether a service or page label value can name targets on a status page: it is
// put into queries, so no quotes, backslashes or control characters.
func labelValueOK(s string) bool {
	return s != "" && len(s) <= 200 && !strings.ContainsAny(s, "\"\\`") && !strings.ContainsFunc(s, unicode.IsControl)
}

// linkOK reports whether a link may go on a public page: http(s), or mailto for an address.
func linkOK(s string) bool {
	u, err := url.Parse(s)
	if err != nil || strings.ContainsFunc(s, unicode.IsSpace) {
		return false
	}
	switch u.Scheme {
	case "https", "http":
		return u.Host != ""
	case "mailto":
		return u.Opaque != ""
	}
	return false
}

func (c *Catalog) validateStatusPages(bad func(string, ...any)) {
	names, hosts, defaults := map[string]bool{}, map[string]string{}, []string{}
	for _, p := range c.StatusPages {
		where := fmt.Sprintf("status page %q", p.Name)
		if !idRe.MatchString(p.Name) {
			bad("%s: the name must be lower case letters, digits and dashes", where)
		}
		if names[p.Name] {
			bad("%s is listed twice", where)
		}
		names[p.Name] = true
		if p.Default {
			defaults = append(defaults, p.Name)
		}
		checkText := func(what string, t Text, limit int, required bool) {
			if required && t.Empty() {
				bad("%s: %s is required", where, what)
			}
			for _, k := range sortedTextKeys(t) {
				v := t[k]
				if k != "" && !slices.Contains(StatusLanguages, k) {
					bad("%s: %s: %q is not a language; use %s", where, what, k, strings.Join(StatusLanguages, " or "))
				}
				if utf8.RuneCountInString(v) > limit || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) {
					bad("%s: %s must be at most %d characters, without control characters", where, what, limit)
				}
			}
		}
		checkText("title", p.Title, 120, true)
		checkText("description", p.Description, 1000, false)
		if p.Host != "" {
			if !hostRe.MatchString(p.Host) || len(p.Host) > 253 {
				bad("%s: host %q is not a host name such as status.example.com (lower case, no port, no scheme)", where, p.Host)
			} else if other, ok := hosts[p.Host]; ok {
				bad("%s: host %q is the host of status page %q too", where, p.Host, other)
			}
			hosts[p.Host] = p.Name
		}
		if p.Brand != "" && !slices.Contains(StatusBrands, p.Brand) {
			bad("%s: brand must be one of %s", where, strings.Join(StatusBrands, ", "))
		}
		seenLang := map[string]bool{}
		for _, l := range p.Languages {
			if !slices.Contains(StatusLanguages, l) || seenLang[l] {
				bad("%s: languages are en and zh, each once, the default first", where)
				break
			}
			seenLang[l] = true
		}
		for _, a := range p.Alerts {
			if !alertNameRe.MatchString(a) {
				bad("%s: alerts: %q is not an alert name such as ServiceDown", where, a)
			}
		}
		if p.Notes != "" && p.Notes != "public" && p.Notes != "all" {
			bad("%s: notes is public (only notes that begin with \"public:\", the default) or all", where)
		}
		if len(p.Components) == 0 {
			bad("%s needs at least one component", where)
		}
		comps := map[string]bool{}
		for _, cm := range p.Components {
			cw := fmt.Sprintf("%s: component %q", where, cm.Name)
			if !idRe.MatchString(cm.Name) {
				bad("%s: the name must be lower case letters, digits and dashes", cw)
			}
			if comps[cm.Name] {
				bad("%s is listed twice", cw)
			}
			comps[cm.Name] = true
			checkText("component "+cm.Name+"'s title", cm.Title, 120, true)
			checkText("component "+cm.Name+"'s description", cm.Description, 500, false)
			if len(cm.Services) == 0 && cm.Page == "" {
				bad("%s names no services: give services (the service labels of its probe targets) or page (a page label on them)", cw)
			}
			for _, s := range cm.Services {
				if !labelValueOK(s) {
					bad("%s: service %q cannot name targets (no quotes, backslashes or control characters)", cw, s)
				}
			}
			if cm.Page != "" && !labelValueOK(cm.Page) {
				bad("%s: page %q cannot name targets (no quotes, backslashes or control characters)", cw, cm.Page)
			}
		}
		grouped := map[string]bool{}
		for i, g := range p.Groups {
			checkText(fmt.Sprintf("group %d's title", i+1), g.Title, 120, true)
			checkText(fmt.Sprintf("group %d's description", i+1), g.Description, 500, false)
			for _, n := range g.Components {
				switch {
				case !comps[n]:
					bad("%s: group %q lists component %q, which the page does not have", where, g.Title.In(""), n)
				case grouped[n]:
					bad("%s: component %q is in two groups", where, n)
				}
				grouped[n] = true
			}
		}
		for _, l := range [][2]string{{"home", p.Links.Home}, {"docs", p.Links.Docs}, {"abuse", p.Links.Abuse}, {"privacy", p.Links.Privacy}} {
			if l[1] != "" && !linkOK(l[1]) {
				bad("%s: links.%s %q is not an http(s) or mailto link", where, l[0], l[1])
			}
		}
		checkText("menu's title", p.Menu.Title, 60, false)
		if len(p.Menu.Groups) > 0 && p.Menu.Title.Empty() {
			bad("%s: the menu needs a title, the name its button shows", where)
		}
		for i, g := range p.Menu.Groups {
			checkText(fmt.Sprintf("menu group %d's title", i+1), g.Title, 60, false)
			if len(g.Links) == 0 {
				bad("%s: menu group %d has no links", where, i+1)
			}
			for _, l := range g.Links {
				checkText("a menu link's title", l.Title, 60, true)
				if !linkOK(l.URL) {
					bad("%s: menu link %q is not an http(s) or mailto link", where, l.URL)
				}
			}
		}
	}
	if len(defaults) > 1 {
		bad("status pages %s are all marked default; one is served at /status", strings.Join(defaults, ", "))
	}
}
