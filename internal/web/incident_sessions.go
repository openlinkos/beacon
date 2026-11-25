package web

import (
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// incidentSessions adds recordings on designated machines only to the signed-in session view.
func (s *Server) incidentSessions(ev bridge.Evidence, a access) ([]sessionRow, string) {
	views := slices.Clone(ev.Done.Sessions)
	private := map[string]bool{}
	if v := a.Viewer; v != nil && v.person() && v.hasAny("sessions:read|machines:ssh") && s.SSH != nil {
		for _, r := range s.designatedSessionRecords(ev) {
			var ended *time.Time
			if !r.Ended.IsZero() {
				ended = &r.Ended
			}
			views = append(views, bridge.SessionView{ID: r.ID, Agent: r.Agent, Via: r.Via,
				Machine: r.Machine, Kind: r.Kind, Started: r.Started, Ended: ended, Exit: r.Exit, Commands: r.Commands})
			private[r.ID] = true
		}
		if len(private) > 0 {
			slices.SortStableFunc(views, func(a, b bridge.SessionView) int { return a.Started.Compare(b.Started) })
		}
	}
	rows := s.sessions(views, a, private)
	if len(rows) == 0 {
		return rows, ""
	}
	machines, commands := map[string]bool{}, 0
	for i, v := range views {
		machines[v.Machine] = true
		switch v.Kind {
		case "shell":
			commands += max(v.Commands, len(rows[len(rows)-1-i].Commands))
		case "exec", "file":
			commands++
		}
	}
	return rows, s.t("%d sessions on %d machines, %d commands", len(rows), len(machines), commands)
}

func (s *Server) designatedSessionRecords(ev bridge.Evidence) []store.SessionRecord {
	machines := map[string]store.Machine{}
	for _, binding := range ev.MonitorMachines {
		if m, ok := s.Store.Machine(binding.Name); ok && m.Registration() == binding.Registration {
			machines[m.Name] = m
		}
	}
	if len(machines) == 0 {
		return nil
	}
	listed := map[string]bool{}
	for _, v := range ev.Done.Sessions {
		listed[v.ID] = true
	}
	records := s.Store.SessionsBetween(ev.Window.From, ev.Window.To, 1000, func(r store.SessionRecord) bool {
		m, ok := machines[r.Machine]
		return ok && r.MachineRegistration == m.Registration() && !r.Started.Before(m.Added) && !listed[r.ID]
	})
	const limit = 20
	return records[max(0, len(records)-limit):]
}
