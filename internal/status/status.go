// Package status computes the public status pages from what Beacon already has: the pages' definitions
// in the catalogue (catalog.StatusPage), the probes' results in VictoriaMetrics (history.go), the alert
// groups as incidents, and the maintenance windows. Package web serves them as HTML, JSON, an Atom feed
// and badges, without signing in; the API and MCP hand agents the same JSON.
//
// A component's state now is the worst of: a maintenance window on its services; the incidents firing on
// them (ServiceDown is down, the other availability alerts degraded); and the probes' agreement in the
// last minutes, by the reference rules' logic. So the page never says a service works while an alert
// about it fires, and shows a failure the probes agree on a minute or two before the alert, which waits
// to be sure. Its history is 90 days of that agreement, minute by minute, with the incidents and the
// maintenance windows laid over it.
package status

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// ErrNotFound means there is no such page, incident or component.
var ErrNotFound = errors.New("no such status page")

const (
	// cacheFor is how long a computed page is served before it is computed again.
	cacheFor = 30 * time.Second
	// recent is how far back the page lists incidents and maintenance that ended.
	recent = 30 * 24 * time.Hour
	// tick is how often Run keeps the incidents and the history up to date.
	tick = time.Minute
)

// Deps are what the pages are made from. VM may be nil (then there is no probe data); PublicURL is
// where Beacon is, for the pages' links; Location is where days begin.
type Deps struct {
	Config    *configdb.Service
	Bridge    *bridge.Bridge
	Store     *store.Store
	VM        Querier
	PublicURL string
	Location  *time.Location
	Log       *slog.Logger
	Now       func() time.Time
}

// Service computes the pages and keeps them for a little while.
type Service struct {
	d    Deps
	hist *history

	mu    sync.Mutex
	pages map[string]cached // by page and language
}

type cached struct {
	page *Page
	at   time.Time
}

// New makes the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{d: d, hist: newHistory(), pages: map[string]cached{}}
}

func (s *Service) catalog() *catalog.Catalog { return s.d.Config.Catalog() }

// Def returns the definition of the page called name; "" is the default page.
func (s *Service) Def(name string) (catalog.StatusPage, bool) {
	if s == nil || s.d.Config == nil {
		return catalog.StatusPage{}, false
	}
	if name == "" {
		return s.catalog().DefaultStatusPage()
	}
	return s.catalog().StatusPage(name)
}

// ByHost returns the definition of the page served at host.
func (s *Service) ByHost(host string) (catalog.StatusPage, bool) {
	if s == nil || s.d.Config == nil {
		return catalog.StatusPage{}, false
	}
	return s.catalog().StatusPageByHost(host)
}

// Lang is the language a page is shown in when asked for want: want if the page has it, else its first.
func Lang(p catalog.StatusPage, want string) string {
	if l, ok := i18n.Parse(want); ok && slices.Contains(p.Langs(), string(l)) {
		return string(l)
	}
	return p.Langs()[0]
}

// Page returns the page called name in the language asked for ("": the page's default), computed at
// most cacheFor ago.
func (s *Service) Page(ctx context.Context, name, lang string) (*Page, error) {
	def, ok := s.Def(name)
	if !ok {
		return nil, ErrNotFound
	}
	lang = Lang(def, lang)
	key := def.Name + "\x00" + lang
	now := s.d.Now()
	s.mu.Lock()
	c, ok := s.pages[key]
	s.mu.Unlock()
	if ok && now.Sub(c.at) < cacheFor && c.page.version == s.d.Config.Version() {
		return c.page, nil
	}
	p := s.compute(ctx, def, lang, now)
	s.mu.Lock()
	s.pages[key] = cached{page: p, at: now}
	s.mu.Unlock()
	return p, nil
}

// Run keeps the incidents and the probes' history up to date, so that pages are quick and an incident
// outlives its alert group.
func (s *Service) Run(ctx context.Context) {
	s.sync()
	_ = s.refresh(ctx, 0) // the history of the last 90 days, at the start
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sync()
			if err := s.refresh(ctx, 0); err != nil {
				s.d.Log.Warn("status pages: reading the probes' history failed", "err", err)
			}
		}
	}
}

// sync keeps a snapshot of every alert group that may be an incident, so that it outlives the group.
func (s *Service) sync() {
	if s.d.Bridge == nil || s.d.Store == nil {
		return
	}
	if _, err := s.d.Store.PutIncidents(s.d.Bridge.Incidents(), s.d.Now().Add(-store.KeepStatus)); err != nil {
		s.d.Log.Warn("status pages: keeping the incidents failed", "err", err)
	}
}

// services are the services of every page, as the configuration in force names them.
func (s *Service) services() []string {
	st := s.d.Config.State()
	var all []string
	for _, p := range st.Catalog.StatusPages {
		for _, c := range p.Components {
			all = append(all, st.PageServices(c)...)
		}
	}
	return uniq(all)
}

func (s *Service) refresh(ctx context.Context, limit int) error {
	if s.d.Config == nil {
		return nil
	}
	return s.hist.refresh(ctx, s.d.VM, s.services(), s.d.Now(), s.d.Location, limit)
}

// ---- computing a page

func (s *Service) compute(ctx context.Context, def catalog.StatusPage, lang string, now time.Time) *Page {
	l := i18n.Lang(lang)
	st := s.d.Config.State()
	s.sync()
	if err := s.hist.refresh(ctx, s.d.VM, s.pageServices(st, def), now, s.d.Location, 3); err != nil {
		s.d.Log.Warn("status pages: reading the probes failed", "page", def.Name, "err", err)
	}
	p := &Page{Name: def.Name, Title: def.Title.In(lang), Description: def.Description.In(lang), URL: s.URL(def, ""), Language: lang,
		Languages: def.Langs(), Updated: now.UTC(), Days: Days, Timezone: s.d.Location.String(), Locations: s.locations(now),
		Components: []Component{}, Groups: []Group{}, Incidents: []Incident{}, Maintenance: []Window{}, version: st.Version}
	p.Links = Links{Feed: s.URL(def, "feed.atom"), JSON: s.URL(def, ".json"), Badge: s.URL(def, "badge.svg"),
		Home: def.Links.Home, Docs: def.Links.Docs, Abuse: def.Links.Abuse, Privacy: def.Links.Privacy}
	group := map[string]string{}
	for _, g := range def.Groups {
		p.Groups = append(p.Groups, Group{Title: g.Title.In(lang), Description: g.Description.In(lang), Components: slices.Clone(g.Components)})
		for _, c := range g.Components {
			group[c] = g.Title.In(lang)
		}
	}
	for _, c := range def.Components {
		p.Components = append(p.Components, Component{Name: c.Name, Title: c.Title.In(lang), Description: c.Description.In(lang),
			Group: group[c.Name], services: st.PageServices(c)})
	}
	windows := s.windows(def, p, now)
	incidents := s.incidents(def, p, l, now)
	for i := range p.Components {
		c := &p.Components[i]
		c.History, c.Availability = s.days(c, incidents, windows, now)
		c.Status = s.now(c, incidents, windows, now)
	}
	p.all = incidents
	for _, in := range incidents {
		if in.Status == "active" || in.Resolved == nil || now.Sub(*in.Resolved) <= recent {
			p.Incidents = append(p.Incidents, in)
		}
	}
	for _, w := range windows {
		if w.Status != "completed" || now.Sub(w.Ends) <= recent {
			p.Maintenance = append(p.Maintenance, w.Window)
		}
	}
	p.Status, p.Summary = overall(p, l)
	return p
}

func (s *Service) pageServices(st *configdb.State, def catalog.StatusPage) []string {
	var all []string
	for _, c := range def.Components {
		all = append(all, st.PageServices(c)...)
	}
	return uniq(all)
}

// URL is the address of a page's file ("" the page, ".json" its JSON, "feed.atom", "badge.svg",
// "incidents/<id>", "components/<name>/badge.svg"): on the page's own host when it has one, else under
// /status where Beacon is.
func (s *Service) URL(def catalog.StatusPage, file string) string {
	if def.Host != "" {
		switch file {
		case "":
			return "https://" + def.Host + "/"
		case ".json":
			return "https://" + def.Host + "/status.json"
		}
		return "https://" + def.Host + "/" + file
	}
	base := strings.TrimSuffix(s.d.PublicURL, "/") + "/status/" + def.Name
	switch file {
	case "":
		return base
	case ".json":
		return base + ".json"
	}
	return base + "/" + file
}

// locations are the public names of the probe locations expected to report now.
func (s *Service) locations(now time.Time) []string {
	c := s.catalog()
	out := []string{}
	for _, l := range c.Locations {
		if !c.Expected(l, now) {
			continue
		}
		name := l.Name
		if l.Network != "" {
			name += " (" + l.Network + ")"
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// now is a component's state now.
func (s *Service) now(c *Component, incidents []Incident, windows []window, now time.Time) string {
	for _, w := range windows {
		if w.Status == "active" && slices.Contains(w.Components, c.Name) {
			return Maintenance
		}
	}
	state := NoData
	for _, in := range incidents {
		if in.Status == "active" && slices.Contains(in.Components, c.Name) {
			state = worst(state, in.Impact)
		}
	}
	m, seen := s.hist.latest(now, s.d.Location, c.services)
	switch {
	case m == minDown:
		state = worst(state, Down)
	case m == minDegraded:
		state = worst(state, Degraded)
	case seen:
		state = worst(state, Operational)
	}
	return state
}

// overall is the page's state and the line that says it.
func overall(p *Page, l i18n.Lang) (string, string) {
	state := NoData
	var down, degraded []string
	for _, c := range p.Components {
		state = worst(state, c.Status)
		switch c.Status {
		case Down:
			down = append(down, c.Title)
		case Degraded:
			degraded = append(degraded, c.Title)
		}
	}
	switch {
	case len(down) == 1:
		return state, i18n.T(l, "%s is down", down[0])
	case len(down) > 1:
		return state, i18n.T(l, "%d components are down", len(down))
	case len(degraded) == 1:
		return state, i18n.T(l, "%s is partly down", degraded[0])
	case len(degraded) > 1:
		return state, i18n.T(l, "%d components are partly down", len(degraded))
	case state == Maintenance:
		return state, i18n.T(l, "Maintenance in progress")
	case state == Operational:
		return state, i18n.T(l, "All systems operational")
	}
	return state, i18n.T(l, "No data yet")
}
