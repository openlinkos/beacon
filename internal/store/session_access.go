package store

import (
	"encoding/json"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// SessionAccess is a request's view of session metadata and detail permissions. For a principal whose
// machine access is limited (anything short of an owner's) it captures the current enrolments it covers
// once, so list filters do not need nested reads.
type SessionAccess struct {
	metadata bool
	details  bool
	every    bool
	all      bool
	machines map[string]sessionMachine // the enrolled machines the principal covers
	others   map[string]bool           // the enrolled critical machines it does not cover
}

type sessionMachine struct {
	registration string
	added        time.Time
}

// SessionAccess evaluates session visibility for the principal resolved for this request.
func (s *Store) SessionAccess(p Principal) SessionAccess {
	access := SessionAccess{details: p.Has("sessions:read") || p.Has("machines:ssh")}
	access.metadata = access.details || p.Has("status:read")
	if !access.metadata || p.NoMachines() {
		access.metadata = false
		return access
	}
	if p.IsEvery() {
		access.every = true
		return access
	}
	access.all = p.IsAll()
	access.machines, access.others = map[string]sessionMachine{}, map[string]bool{}
	err := s.db.View(func(tx kv.Tx) error {
		var decodeErr error
		tx.Range(bucketMachines, kv.Range{}, func(_, v []byte) bool {
			var m Machine
			if decodeErr = json.Unmarshal(v, &m); decodeErr != nil {
				return false
			}
			switch {
			case p.MachineAccess.Covers(m.Name, m.Critical):
				access.machines[m.Name] = sessionMachine{registration: m.Registration(), added: m.Added}
			case m.Critical:
				access.others[m.Name] = true
			}
			return true
		})
		return decodeErr
	})
	if err != nil { // nothing is visible when the enrolments cannot be read
		access.all = false
		clear(access.machines)
	}
	return access
}

// Visible reports whether the principal may see the record's session metadata. An owner sees every
// session. Anyone else sees the sessions of the current enrolments their access covers; with access to
// all machines also those of machines no longer enrolled, unless the session was on a critical machine.
func (a SessionAccess) Visible(r SessionRecord) bool {
	switch {
	case !a.metadata:
		return false
	case a.every:
		return true
	}
	m, ok := a.machines[r.Machine]
	if !ok {
		return a.all && !r.Critical && !a.others[r.Machine] // not enrolled now, or critical and not covered
	}
	if r.MachineRegistration != "" {
		if r.MachineRegistration == m.registration {
			return true
		}
		return a.all && !r.Critical
	}
	// Legacy rows lack an enrolment ID. Their timestamps must place them on the current
	// registration; missing timestamps cannot establish that.
	if !r.Started.IsZero() && !m.added.IsZero() && !r.Started.Before(m.added) {
		return true
	}
	return a.all && !r.Critical
}

// Details reports whether the principal may see commands, transcripts and recordings for records
// it can see. Check Visible even when Details is true.
func (a SessionAccess) Details() bool { return a.details }
