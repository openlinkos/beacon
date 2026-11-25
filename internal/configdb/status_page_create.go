package configdb

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openlinkos/beacon/internal/catalog"
)

// ErrStatusPageExists means the requested address already belongs to a page.
var ErrStatusPageExists = errors.New("a status page already uses that name")

// StatusPageDraft is the deliberately small definition created by the console.
type StatusPageDraft struct {
	Name, Title, Language, ComponentTitle string
	Services                              []string
}

// CanCreateStatusPage reports whether creating a page can preserve an existing default address.
// A catalogue without pages can publish its first default here.
func (st *State) CanCreateStatusPage() bool {
	if st == nil || st.Catalog == nil {
		return false
	}
	pages := st.Catalog.StatusPages
	return len(pages) == 0 || slices.ContainsFunc(pages, func(p catalog.StatusPage) bool { return p.Default })
}

// DraftStatusPage validates a routine new page against the state used for its public preview.
func (st *State) DraftStatusPage(in StatusPageDraft) (catalog.StatusPage, error) {
	if !st.CanCreateStatusPage() {
		return catalog.StatusPage{}, fmt.Errorf("%w: mark an existing default status page before creating another", ErrConflict)
	}
	if _, exists := st.Catalog.StatusPage(in.Name); exists {
		return catalog.StatusPage{}, fmt.Errorf("%w: %w", ErrConflict, ErrStatusPageExists)
	}
	if err := st.checkComponentServices(nil, in.Services); err != nil {
		return catalog.StatusPage{}, err
	}
	page := catalog.StatusPage{
		Name: in.Name, Title: catalog.Text{"": strings.TrimSpace(in.Title)},
		Default: len(st.Catalog.StatusPages) == 0, Languages: []string{in.Language}, Notes: "public",
		Components: []catalog.StatusComponent{{
			Name: "services", Title: catalog.Text{"": in.ComponentTitle},
			Services: slices.Sorted(slices.Values(in.Services)),
		}},
	}
	check := catalog.Catalog{StatusPages: []catalog.StatusPage{page}}
	if err := check.Validate(); err != nil {
		return catalog.StatusPage{}, fmt.Errorf("%w: %w", ErrBadInput, err)
	}
	return page, nil
}
