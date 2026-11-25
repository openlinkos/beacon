package status

import (
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/configdb"
)

// MaintenancePublication compares the announcements the public page shows before and after an edit.
// It returns the exact newly visible entries and the durable revision of both announcement sources.
func (s *Service) MaintenancePublication(st *configdb.State, before, after catalog.StatusPage) ([]Window, uint64, error) {
	if s.d.Store == nil {
		return nil, 0, nil
	}
	source, err := s.d.Store.MaintenanceSnapshot()
	if err != nil {
		return nil, 0, err
	}
	now := s.d.Now()
	visible := func(def catalog.StatusPage) []Window {
		page := &Page{}
		for _, c := range def.Components {
			page.Components = append(page.Components, Component{Name: c.Name, services: st.PageServices(c)})
		}
		return listedMaintenance(s.windows(page, now, source), now)
	}
	alreadyPublic := make(map[string]bool)
	for _, window := range visible(before) {
		alreadyPublic[window.ID] = true
	}
	var out []Window
	for _, window := range visible(after) {
		if !alreadyPublic[window.ID] {
			out = append(out, window)
		}
	}
	return out, source.Revision, nil
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
