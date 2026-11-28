package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

var (
	bucketMachineRevisions = []byte("machine_revisions")
	machineUser            = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	ErrMachineVersion      = errors.New("no such version in this machine's current registration")
	ErrMachineUser         = errors.New("invalid account name")
)

func ValidMachineUser(user string) bool { return machineUser.MatchString(user) }

// Registration identifies the machine enrolment independently of its editable settings.
func (m Machine) Registration() string {
	at := m.Added.UTC().Format(time.RFC3339Nano)
	if m.LegacyHistory {
		return "0-" + at
	}
	return strconv.FormatInt(m.HistoryStart, 10) + "-" + at
}

// MonitorMachine records the enrolment designated for a service when an incident opened.
type MonitorMachine struct {
	Service      string `json:"service"`
	Name         string `json:"name"`
	Registration string `json:"registration"`
}

// MachineMetadata is the restorable part of a registration.
type MachineMetadata struct {
	User     string `json:"user"`
	Critical bool   `json:"critical"`
	Note     string `json:"note"`
}

func (m Machine) metadata() MachineMetadata {
	return MachineMetadata{User: m.User, Critical: m.Critical, Note: m.Note}
}

type MachineRevision struct {
	Version  int64           `json:"version"`
	At       time.Time       `json:"time"`
	Actor    string          `json:"actor"`
	Via      string          `json:"via"`
	Action   string          `json:"action"`
	Restored int64           `json:"restored_version,omitempty"`
	Metadata MachineMetadata `json:"metadata"`
}

type MachineHistory struct {
	Version   int64             `json:"version"`
	Revisions []MachineRevision `json:"revisions"`
}

type MachineMetadataUpdate struct {
	Note      *string `json:"note,omitempty"`
	Critical  *bool   `json:"critical,omitempty"`
	User      *string `json:"user,omitempty"`
	IfVersion *int64  `json:"if_version,omitempty"`
}

func machineIn(tx kv.Tx, name string) (Machine, error) {
	var m Machine
	raw := tx.Get(bucketMachines, []byte(name))
	if raw == nil {
		return m, ErrNotFound
	}
	err := json.Unmarshal(raw, &m)
	return m, err
}

func writeMachine(tx kv.Tx, m Machine) error {
	if !MachineName.MatchString(m.Name) {
		return fmt.Errorf("machine name %q: use lower case letters, digits and dashes", m.Name)
	}
	if fp := m.ConnectorFingerprint; m.Connector() && fp != "" {
		if holder := keyHolder(tx, fp); holder != "" {
			return fmt.Errorf("%w: the key %s belongs to %s", ErrExists, fp, holder)
		}
		if other, ok := machineByConnectorKey(tx, fp); ok && other.Name != m.Name {
			return fmt.Errorf("%w: the key %s is machine %s's connector key", ErrExists, fp, other.Name)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return tx.Put(bucketMachines, []byte(m.Name), b)
}

func machineHistoryPrefix(m Machine) []byte {
	return append(append([]byte(m.Name), 0), versionKey(m.HistoryStart)...)
}

func machineRevisionKey(m Machine, v int64) []byte {
	return append(machineHistoryPrefix(m), versionKey(v)...)
}

func (s *Store) startMachineHistory(tx kv.Tx, m *Machine, action, via string) error {
	m.HistoryStart, m.LegacyHistory = 0, false
	at := m.Added
	if at.IsZero() {
		at = s.now().UTC()
	}
	if err := recordMachineRevision(tx, m, MachineRevision{At: at, Actor: m.AddedBy, Via: via, Action: action}); err != nil {
		return err
	}
	return writeMachine(tx, *m)
}

// RegisterMachine atomically starts a new history when a machine is enrolled or explicitly replaced.
// previous is the registration as read before any host key discovery, or nil for a new machine. A machine
// that would be another's second record is refused (ErrMachineAlias, machine_alias.go). A machine
// critical now that was not gives the credentials that name it an end (critical_names.go).
func (s *Store) RegisterMachine(m Machine, previous *Machine, via string) (Machine, error) {
	tokens := false
	err := s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		old, err := machineIn(tx, m.Name)
		switch {
		case err != nil && !errors.Is(err, ErrNotFound):
			return err
		case previous == nil && err == nil:
			return ErrExists
		case previous != nil && (err != nil || old != *previous):
			return ErrConflict
		}
		if err := checkAlias(tx, m); err != nil {
			return err
		}
		if err := s.startMachineHistory(tx, &m, "registered", via); err != nil {
			return err
		}
		if !m.Critical || previous != nil && old.Critical {
			return nil
		}
		tokens, err = s.endCriticalNames(tx, raw, m.Name, s.now(), m.AddedBy, via)
		return err
	})
	if err == nil && tokens {
		err = s.RefreshTokens()
	}
	return m, err
}

func recordMachineRevision(tx kv.Tx, m *Machine, rev MachineRevision) error {
	n, err := tx.NextSequence(bucketMachineRevisions)
	if err != nil {
		return err
	}
	m.MetadataVersion = int64(n)
	if m.HistoryStart == 0 {
		m.HistoryStart = m.MetadataVersion
	}
	rev.Version, rev.Metadata = m.MetadataVersion, m.metadata()
	b, err := json.Marshal(rev)
	if err != nil {
		return err
	}
	return tx.Put(bucketMachineRevisions, machineRevisionKey(*m, rev.Version), b)
}

// UpdateMachineIdentity mutates the current registration in a write transaction. A caller can
// install connector or host keys alongside metadata updates without writing a stale machine back. A
// change that makes the machine another's second record is refused (ErrMachineAlias).
func (s *Store) UpdateMachineIdentity(name string, same func(Machine) bool, change func(*Machine)) (Machine, error) {
	var m Machine
	err := s.db.Update(func(tx kv.Tx) error {
		var err error
		m, err = machineIn(tx, name)
		if err != nil {
			return err
		}
		if !same(m) {
			return ErrConflict
		}
		change(&m)
		if err := checkAlias(tx, m); err != nil {
			return err
		}
		return writeMachine(tx, m)
	})
	return m, err
}

// MachineMetadataHistory returns the most recent snapshots of the current registration and its
// version in the same read transaction. Older enrolments and their keys are outside this history.
func (s *Store) MachineMetadataHistory(name string, before int64, limit int) (MachineHistory, error) {
	out := MachineHistory{Revisions: []MachineRevision{}}
	err := s.db.View(func(tx kv.Tx) error {
		m, err := machineIn(tx, name)
		if err != nil {
			return err
		}
		out.Version = m.MetadataVersion
		r := kv.Range{Reverse: true, Prefix: machineHistoryPrefix(m)}
		if before > 0 {
			r.Start = machineRevisionKey(m, before)
		}
		var decodeErr error
		tx.Range(bucketMachineRevisions, r, func(_ []byte, b []byte) bool {
			var item MachineRevision
			if decodeErr = json.Unmarshal(b, &item); decodeErr != nil {
				return false
			}
			out.Revisions = append(out.Revisions, item)
			return len(out.Revisions) < limit
		})
		return decodeErr
	})
	return out, err
}

// PatchMachineMetadata applies an edit or copies an older snapshot under the expected version. The
// returned before and after, and the new revision, describe the same atomic update. check, if given,
// decides in the same transaction whether the change from before to after may be made; its error is
// returned and nothing is written. A machine marked critical gives the credentials that name it an end
// (critical_names.go), in the same transaction.
func (s *Store) PatchMachineMetadata(name string, patch MachineMetadataUpdate, restore int64, actor, via string, check func(before, after Machine) error) (Machine, Machine, error) {
	var before, after Machine
	tokens := false
	err := s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		var err error
		before, err = machineIn(tx, name)
		if err != nil {
			return err
		}
		if restore > 0 && patch.IfVersion == nil || patch.IfVersion != nil && *patch.IfVersion != before.MetadataVersion {
			return ErrConflict
		}
		after = before
		rev := MachineRevision{At: s.now().UTC(), Actor: actor, Via: via, Action: "edited", Restored: restore}
		if restore > 0 {
			b := tx.Get(bucketMachineRevisions, machineRevisionKey(before, restore))
			if b == nil {
				return ErrMachineVersion
			}
			var prev MachineRevision
			if err := json.Unmarshal(b, &prev); err != nil {
				return err
			}
			after.User, after.Critical, after.Note = prev.Metadata.User, prev.Metadata.Critical, prev.Metadata.Note
			rev.Action = "restored"
		} else {
			patch.apply(&after)
		}
		if (restore > 0 || patch.User != nil) && !ValidMachineUser(after.User) {
			return ErrMachineUser
		}
		if after.metadata() == before.metadata() {
			return nil
		}
		if check != nil {
			if err := check(before, after); err != nil {
				return err
			}
		}
		if after.HistoryStart == 0 { // installations that already had machines gain a baseline before the first edit
			base := before
			if err := recordMachineRevision(tx, &base, MachineRevision{At: rev.At, Action: "baseline"}); err != nil {
				return err
			}
			after.HistoryStart, after.LegacyHistory = base.HistoryStart, true
		}
		if err := recordMachineRevision(tx, &after, rev); err != nil {
			return err
		}
		if err := writeMachine(tx, after); err != nil {
			return err
		}
		event := machineMetadataAudit(before, after, rev)
		if err := s.audit.AppendTx(raw, &event); err != nil {
			return err
		}
		if after.Critical && !before.Critical { // the names of it given while it was not take an end
			tokens, err = s.endCriticalNames(tx, raw, name, rev.At, actor, via)
		}
		return err
	})
	if err == nil && tokens {
		err = s.RefreshTokens()
	}
	return before, after, err
}

func machineMetadataAudit(before, after Machine, rev MachineRevision) events.Event {
	event := events.Event{Time: rev.At, Actor: rev.Actor, Via: rev.Via, Action: "machine_update", Object: after.Name}
	if rev.Restored > 0 {
		event.Action = "machine_restore"
		event.Detail = fmt.Sprintf("restored version %d as %d", rev.Restored, after.MetadataVersion)
	} else {
		event.Detail = fmt.Sprintf("version %d; note %q -> %q; critical %v -> %v; user %s -> %s", after.MetadataVersion,
			before.Note, after.Note, before.Critical, after.Critical, before.User, after.User)
	}
	return event
}

func (p MachineMetadataUpdate) apply(m *Machine) {
	if p.User != nil {
		m.User = *p.User
	}
	if p.Note != nil {
		m.Note = strings.TrimSpace(*p.Note)
	}
	if p.Critical != nil {
		m.Critical = *p.Critical
	}
}
