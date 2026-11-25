package store

import (
	"errors"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// SessionAccess is a request's view of session metadata and detail permissions. For machine-scoped
// principals it captures the current enrolments once, so list filters do not need nested reads.
type SessionAccess struct {
	metadata bool
	details  bool
	machines map[string]sessionMachine
}

type sessionMachine struct {
	registration string
	added        time.Time
}

// SessionAccess evaluates session visibility for the principal resolved for this request.
func (s *Store) SessionAccess(p Principal) SessionAccess {
	access := SessionAccess{details: p.Has("sessions:read") || p.Has("machines:ssh")}
	access.metadata = access.details || p.Has("status:read")
	if !access.metadata || p.MachineScopeEmpty() {
		access.metadata = false
		return access
	}
	if len(p.Machines) == 0 {
		return access
	}
	access.machines = make(map[string]sessionMachine, len(p.Machines))
	err := s.db.View(func(tx kv.Tx) error {
		for _, name := range p.Machines {
			m, err := machineIn(tx, name)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			access.machines[name] = sessionMachine{registration: m.Registration(), added: m.Added}
		}
		return nil
	})
	if err != nil {
		clear(access.machines)
	}
	return access
}

// Visible reports whether the principal may see the record's session metadata.
func (a SessionAccess) Visible(r SessionRecord) bool {
	if !a.metadata {
		return false
	}
	if a.machines == nil {
		return true
	}
	m, ok := a.machines[r.Machine]
	if !ok {
		return false
	}
	if r.MachineRegistration != "" {
		return r.MachineRegistration == m.registration
	}
	// Legacy rows lack an enrolment ID. Their timestamps must place them on the current
	// registration; missing timestamps cannot establish that.
	return !r.Started.IsZero() && !m.added.IsZero() && !r.Started.Before(m.added)
}

// Details reports whether the principal may see commands, transcripts and recordings for records
// it can see. Check Visible even when Details is true.
func (a SessionAccess) Details() bool { return a.details }
