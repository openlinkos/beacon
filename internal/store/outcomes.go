package store

import (
	"encoding/json"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

var bucketOutcomes = []byte("incident_outcomes")

// IncidentOutcome is the bounded internal record of one resolved alert episode.
// Its ID starts with the UTC resolution timestamp so the bucket is ordered newest-first.
type IncidentOutcome struct {
	ID            string          `json:"id"`
	Group         string          `json:"group"`
	Title         string          `json:"title"`
	Severity      string          `json:"severity"`
	Services      []string        `json:"services"`
	Started       time.Time       `json:"started_at"`
	Resolved      time.Time       `json:"resolved_at"`
	AuditUntil    time.Time       `json:"audit_until,omitzero"`
	Participation []Participation `json:"participation"`
	Verification  *Verification   `json:"verification,omitempty"`
	Sessions      []string        `json:"related_sessions,omitempty"`
}

// Participation is the latest alert action recorded for one kind of actor, with no note text.
type Participation struct {
	Kind   string    `json:"kind"` // person, agent or unattributed
	By     string    `json:"by"`
	Via    string    `json:"via"`
	Action string    `json:"action"`
	At     time.Time `json:"at"`
}

// Verification counts the target-location observations of the latest on-demand check.
type Verification struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	Via     string    `json:"via"`
	Passed  int       `json:"passed"`
	Failed  int       `json:"failed"`
	Missing int       `json:"missing"`
}

func putOutcome(tx kv.Tx, outcome IncidentOutcome) error {
	raw, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	return tx.Put(bucketOutcomes, []byte(outcome.ID), raw)
}

// EnsureIncidentOutcome backfills a missing summary from a group saved by an older Beacon.
func (s *Store) EnsureIncidentOutcome(outcome IncidentOutcome) error {
	return s.db.Update(func(tx kv.Tx) error {
		if tx.Get(bucketOutcomes, []byte(outcome.ID)) != nil {
			return nil
		}
		return putOutcome(tx, outcome)
	})
}

// DropResolvedGroup preserves the compact outcome, then removes the rich alert group atomically.
func (s *Store) DropResolvedGroup(g Group, outcome *IncidentOutcome) error {
	return s.db.Update(func(tx kv.Tx) error {
		raw := tx.Get(bucketGroups, []byte(g.ID))
		if raw == nil {
			return nil
		}
		var current Group
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		if current.Firing() || current.Episode != g.Episode || !current.ResolvedAt.Equal(g.ResolvedAt) || !current.Updated.Equal(g.Updated) {
			return kv.ErrCompareFailed
		}
		if outcome != nil {
			if err := putOutcome(tx, *outcome); err != nil {
				return err
			}
		}
		return tx.Delete(bucketGroups, []byte(g.ID))
	})
}

// IncidentOutcome returns a retained summary under its episode ID.
func (s *Store) IncidentOutcome(id string, since time.Time) (IncidentOutcome, error) {
	var got IncidentOutcome
	err := s.db.View(func(tx kv.Tx) error {
		raw := tx.Get(bucketOutcomes, []byte(id))
		if raw == nil {
			return ErrNotFound
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			return err
		}
		if got.Resolved.Before(since) {
			return ErrNotFound
		}
		return nil
	})
	return got, err
}

// OutcomeFilter pages backwards in resolution time, optionally matching one recurring alert group.
type OutcomeFilter struct {
	Since  time.Time
	Before string
	Group  string
	Limit  int
}

// IncidentOutcomes returns up to Limit+1 rows. The extra row tells callers whether to offer another page.
func (s *Store) IncidentOutcomes(q OutcomeFilter) ([]IncidentOutcome, error) {
	out := make([]IncidentOutcome, 0, q.Limit+1)
	err := s.db.View(func(tx kv.Tx) error {
		var decodeErr error
		tx.Range(bucketOutcomes, kv.Range{Reverse: true, Start: []byte(q.Before)}, func(_, value []byte) bool {
			var item IncidentOutcome
			if decodeErr = json.Unmarshal(value, &item); decodeErr != nil || item.Resolved.Before(q.Since) {
				return false
			}
			if q.Group == "" || q.Group == item.Group {
				out = append(out, item)
			}
			return len(out) <= q.Limit
		})
		return decodeErr
	})
	return out, err
}

// PruneIncidentOutcomes removes summaries older than the configured resolution-time retention.
func (s *Store) PruneIncidentOutcomes(since time.Time) error {
	return s.db.Update(func(tx kv.Tx) error {
		var keys [][]byte
		prefix := []byte(outcomeTime(since))
		tx.Range(bucketOutcomes, kv.Range{}, func(k, _ []byte) bool {
			if string(k) >= string(prefix) {
				return false
			}
			keys = append(keys, bytesClone(k))
			return true
		})
		for _, k := range keys {
			if err := tx.Delete(bucketOutcomes, k); err != nil {
				return err
			}
		}
		return nil
	})
}

func outcomeTime(t time.Time) string { return t.UTC().Format("20060102T150405.000000000") }

// OutcomeID names a resolved episode independently of the live group's ID.
func OutcomeID(resolved time.Time, group, episode string) string {
	return outcomeTime(resolved) + "-" + group + "-" + episode
}
