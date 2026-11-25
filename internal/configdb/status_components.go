package configdb

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/openlinkos/beacon/internal/catalog"
)

// StatusComponentEdit is a routine change to one component on an existing public status page.
// Title is supplied on add. Services replaces the explicit bindings on add or edit.
// Remove selects the component by name and ignores the other fields.
type StatusComponentEdit struct {
	Component string
	Title     string
	Services  []string
	Remove    bool
}

// EditStatusComponent drafts a page from this snapshot. Callers can publish it using PutIf
// with this snapshot's Version.
func (st *State) EditStatusComponent(page string, edit StatusComponentEdit) (catalog.StatusPage, error) {
	if st == nil || st.Catalog == nil {
		return catalog.StatusPage{}, fmt.Errorf("%w: no status page %q", ErrNotFound, page)
	}
	current, ok := st.Catalog.StatusPage(page)
	if !ok {
		return catalog.StatusPage{}, fmt.Errorf("%w: no status page %q", ErrNotFound, page)
	}
	if !catalog.ValidName(edit.Component) {
		return catalog.StatusPage{}, fmt.Errorf("%w: a component name uses lower case letters, digits and dashes", ErrBadInput)
	}
	i := slices.IndexFunc(current.Components, func(c catalog.StatusComponent) bool { return c.Name == edit.Component })
	if i < 0 && (edit.Remove || edit.Title == "") {
		return catalog.StatusPage{}, fmt.Errorf("%w: no component %q on status page %q", ErrNotFound, edit.Component, page)
	}

	draft := cloneStatusPage(current)
	if edit.Remove {
		if len(draft.Components) == 1 {
			return catalog.StatusPage{}, fmt.Errorf("%w: a status page needs at least one component", ErrBadInput)
		}
		draft.Components = slices.Delete(draft.Components, i, i+1)
		for j := range draft.Groups {
			g := &draft.Groups[j]
			g.Components = slices.DeleteFunc(g.Components, func(name string) bool { return name == edit.Component })
		}
	} else {
		var component catalog.StatusComponent
		if i >= 0 {
			if edit.Title != "" {
				return catalog.StatusPage{}, fmt.Errorf("%w: component %q already exists", ErrBadInput, edit.Component)
			}
			component = draft.Components[i]
		} else {
			title := strings.TrimSpace(edit.Title)
			if title == "" {
				return catalog.StatusPage{}, fmt.Errorf("%w: a component needs a title", ErrBadInput)
			}
			component = catalog.StatusComponent{Name: edit.Component, Title: catalog.Text{"": title}}
		}
		if err := st.checkComponentServices(component.Services, edit.Services); err != nil {
			return catalog.StatusPage{}, err
		}
		component.Services = slices.Clone(edit.Services)
		if i >= 0 {
			draft.Components[i] = component
		} else {
			draft.Components = append(draft.Components, component)
		}
	}
	check := catalog.Catalog{StatusPages: []catalog.StatusPage{draft}}
	if err := check.Validate(); err != nil {
		return catalog.StatusPage{}, fmt.Errorf("%w: %w", ErrBadInput, err)
	}
	return draft, nil
}

// Existing explicit bindings can remain after their monitor is removed. New bindings name a
// monitored service. Explicit service names may also name targets excluded from the page selector.
func (st *State) checkComponentServices(previous, selected []string) error {
	if len(selected) == 0 {
		return nil
	}
	known := make(map[string]bool)
	if st.Targets != nil {
		for _, file := range st.Targets.Files() {
			for _, group := range st.Targets.Groups(file) {
				if service := group.Labels["service"]; service != "" && len(group.Targets) > 0 {
					known[service] = true
				}
			}
		}
	}
	seen := make(map[string]bool, len(selected))
	for _, service := range selected {
		if seen[service] {
			return fmt.Errorf("%w: service %q is selected twice", ErrBadInput, service)
		}
		if !known[service] && !slices.Contains(previous, service) {
			return fmt.Errorf("%w: service %q has no probe target", ErrBadInput, service)
		}
		seen[service] = true
	}
	return nil
}

func cloneStatusPage(p catalog.StatusPage) catalog.StatusPage {
	p.Title, p.Description = maps.Clone(p.Title), maps.Clone(p.Description)
	p.Languages, p.Alerts = slices.Clone(p.Languages), slices.Clone(p.Alerts)
	p.Components = slices.Clone(p.Components)
	for i := range p.Components {
		c := &p.Components[i]
		c.Title, c.Description = maps.Clone(c.Title), maps.Clone(c.Description)
		c.Services = slices.Clone(c.Services)
	}
	p.Groups = slices.Clone(p.Groups)
	for i := range p.Groups {
		g := &p.Groups[i]
		g.Title, g.Description = maps.Clone(g.Title), maps.Clone(g.Description)
		g.Components = slices.Clone(g.Components)
	}
	p.Menu.Title = maps.Clone(p.Menu.Title)
	p.Menu.Groups = slices.Clone(p.Menu.Groups)
	for i := range p.Menu.Groups {
		g := &p.Menu.Groups[i]
		g.Title = maps.Clone(g.Title)
		g.Links = slices.Clone(g.Links)
		for j := range g.Links {
			g.Links[j].Title = maps.Clone(g.Links[j].Title)
		}
	}
	return p
}
