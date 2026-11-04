package configdb

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/openlinkos/beacon/internal/catalog"
)

// Status pages are part of the catalogue (catalog/*.yaml, under status_pages) and items of their own,
// stored as status_page/<name>, so that a page is changed, versioned and reverted like a heartbeat.
// The catalogue's checks say whether a page is well formed; the warnings here say what it names that
// no probe target has, which is no error: targets are often added after the page that shows them.

// statusPage reads a status page given as JSON in the shape of its file, for the page called key.
func statusPage(raw json.RawMessage, key string) (catalog.StatusPage, error) {
	var p catalog.StatusPage
	if err := strict(raw, "status page {title, components: [{name, title, services}], ...}", &p); err != nil {
		return p, err
	}
	if p.Name == "" {
		p.Name = key
	}
	if p.Name != key {
		return p, fmt.Errorf("%w: the status page's name is %q, not %q as in the path", ErrBadInput, p.Name, key)
	}
	return p, nil
}

// PageServices are the services that back a status page's component: those it names, and those of the
// probe targets labelled with its page label, unless such a target is labelled visibility: internal.
func (st *State) PageServices(c catalog.StatusComponent) []string {
	out := slices.Clone(c.Services)
	if c.Page != "" && st.Targets != nil {
		for _, f := range st.Targets.Files() {
			for _, g := range st.Targets.Groups(f) {
				s := g.Labels["service"]
				if g.Labels["page"] == c.Page && g.Labels["visibility"] != "internal" && s != "" && !slices.Contains(out, s) {
					out = append(out, s)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// State is the configuration in force, for what reads more of it than the catalogue and the targets.
func (s *Service) State() *State { return s.state.Load() }

// pageWarnings lists the status page components that no probe target backs: a service no target has,
// or a page label no public target carries. Such a component shows "no data".
func pageWarnings(st *State) []string {
	if st.Targets == nil {
		return nil
	}
	known := map[string]bool{}
	for _, f := range st.Targets.Files() {
		for _, g := range st.Targets.Groups(f) {
			known[g.Labels["service"]] = true
		}
	}
	var out []string
	for _, p := range st.Catalog.StatusPages {
		for _, c := range p.Components {
			for _, s := range c.Services {
				if !known[s] {
					out = append(out, fmt.Sprintf("status page %q: component %q names service %q, which no probe target has; it shows no data until one does",
						p.Name, c.Name, s))
				}
			}
			if c.Page != "" && len(st.PageServices(catalog.StatusComponent{Page: c.Page})) == 0 {
				out = append(out, fmt.Sprintf("status page %q: component %q names page label %q, which no public probe target carries; it shows no data until one does",
					p.Name, c.Name, c.Page))
			}
		}
	}
	return out
}
