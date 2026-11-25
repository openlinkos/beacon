package status

import (
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
)

// PublicationNote is an existing incident update the proposed page would newly disclose.
type PublicationNote struct {
	IncidentID, Title string
	Update            Update
}

// Publication holds the newly visible content and the revision the publication must check.
type Publication struct {
	Windows  []Window
	Notes    []PublicationNote
	Revision uint64
}

// ReviewPublication compares the retained public announcements and incident notes before and after an edit.
func (s *Service) ReviewPublication(st *configdb.State, before, after catalog.StatusPage, lang i18n.Lang) (Publication, error) {
	if s.d.Store == nil {
		return Publication{}, nil
	}
	if err := s.syncIncidents(); err != nil {
		return Publication{}, err
	}
	source, records, err := s.d.Store.StatusPublicationSnapshot()
	if err != nil {
		return Publication{}, err
	}
	now := s.d.Now()
	visible := func(def catalog.StatusPage) ([]Window, []Incident) {
		page := &Page{}
		for _, c := range def.Components {
			page.Components = append(page.Components, Component{Name: c.Name, Title: c.Title.In(string(lang)), services: st.PageServices(c)})
		}
		return listedMaintenance(s.windows(page, now, source), now), s.incidentsFrom(def, page, lang, now, records)
	}
	priorWindows, priorIncidents := visible(before)
	nextWindows, nextIncidents := visible(after)
	out := Publication{Revision: source.Revision}
	alreadyPublic := make(map[string]bool)
	for _, window := range priorWindows {
		alreadyPublic[window.ID] = true
	}
	for _, window := range nextWindows {
		if !alreadyPublic[window.ID] {
			out.Windows = append(out.Windows, window)
		}
	}
	type incidentUpdate struct {
		ID string
		Update
	}
	notes := make(map[incidentUpdate]bool)
	for _, in := range priorIncidents {
		for _, u := range in.Updates {
			if u.Kind == "note" {
				notes[incidentUpdate{in.ID, u}] = true
			}
		}
	}
	for _, in := range nextIncidents {
		for _, u := range in.Updates {
			if u.Kind == "note" && !notes[incidentUpdate{in.ID, u}] {
				out.Notes = append(out.Notes, PublicationNote{IncidentID: in.ID, Title: in.Title, Update: u})
			}
		}
	}
	return out, nil
}

func listedMaintenance(windows []window, now time.Time) []Window {
	var out []Window
	for _, w := range windows {
		if w.Status != "completed" || now.Sub(w.Ends) <= recent {
			out = append(out, w.Window)
		}
	}
	return out
}
