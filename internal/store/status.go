package store

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// What the public status pages need to remember for longer than Beacon keeps alert groups and
// silences (7 days after they end): the incidents, as snapshots of the alert groups they came from, and
// the maintenance windows. Both are kept for KeepStatus after they end, as long as a page's history
// reaches. The buckets are made when first written, so a database from an older version needs nothing.

var (
	bucketIncidents   = []byte("status_incidents")
	bucketMaintenance = []byte("maintenance")
)

// KeepStatus is how long an incident or a maintenance window stays after it ended.
const KeepStatus = 91 * 24 * time.Hour

// Incident is an alert group as a public status page may show it, its text already cleaned. Services
// are the services of all its alerts; Firing those of the alerts firing now (none once it resolved).
// Notes are every note on it; the page decides which to publish.
type Incident struct {
	ID         string         `json:"id"`
	AlertName  string         `json:"alertname"`
	Severity   string         `json:"severity"`
	Status     string         `json:"status"` // firing or resolved
	Services   []string       `json:"services"`
	Firing     []string       `json:"firing,omitempty"`
	StartsAt   time.Time      `json:"starts_at"`
	ResolvedAt time.Time      `json:"resolved_at,omitzero"`
	AckedAt    time.Time      `json:"acked_at,omitzero"`
	Notes      []IncidentNote `json:"notes,omitempty"`
}

// IncidentNote is a note on an incident: when, what, and whether an agent (a token through the API or
// MCP) or a person (the console, a card) left it.
type IncidentNote struct {
	At    time.Time `json:"at"`
	Text  string    `json:"text"`
	Agent bool      `json:"agent,omitempty"`
}

// Ended is when the incident resolved, or the zero time while it fires.
func (i Incident) Ended() time.Time {
	if i.Status == "firing" {
		return time.Time{}
	}
	return i.ResolvedAt
}

// PutIncidents saves the incidents that changed and forgets those that ended before forget. It reports
// whether anything was written.
func (s *Store) PutIncidents(in []Incident, forget time.Time) (bool, error) {
	wrote := false
	err := s.db.Update(func(tx kv.Tx) error {
		for _, i := range in {
			b, err := json.Marshal(i)
			if err != nil {
				return err
			}
			if old := tx.Get(bucketIncidents, []byte(i.ID)); bytes.Equal(old, b) {
				continue
			}
			if err := tx.Put(bucketIncidents, []byte(i.ID), b); err != nil {
				return err
			}
			wrote = true
		}
		var old [][]byte
		tx.Range(bucketIncidents, kv.Range{}, func(k, v []byte) bool {
			var i Incident
			if json.Unmarshal(v, &i) == nil && !i.Ended().IsZero() && i.Ended().Before(forget) {
				old = append(old, append([]byte(nil), k...))
			}
			return true
		})
		for _, k := range old {
			if err := tx.Delete(bucketIncidents, k); err != nil {
				return err
			}
			wrote = true
		}
		return nil
	})
	return wrote, err
}

// Incidents lists the incidents kept, the newest first.
func (s *Store) Incidents() []Incident {
	var out []Incident
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketIncidents, kv.Range{}, func(_, v []byte) bool {
			var i Incident
			if json.Unmarshal(v, &i) == nil {
				out = append(out, i)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(a, b int) bool {
		if !out[a].StartsAt.Equal(out[b].StartsAt) {
			return out[a].StartsAt.After(out[b].StartsAt)
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// Maintenance is a maintenance window: the services it covers, when, what for (Title and Description
// are public, on the status pages), who planned it and through what, and the silences that keep its
// alerts from paging people meanwhile. Ended is when it was ended early, and Cancelled says it was
// called off before it began.
type Maintenance struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Services    []string  `json:"services"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	CreatedBy   string    `json:"created_by"`
	Via         string    `json:"via"`
	Created     time.Time `json:"created"`
	Silences    []string  `json:"silences,omitempty"`
	Ended       time.Time `json:"ended,omitzero"`
	Cancelled   bool      `json:"cancelled,omitempty"`
}

// End is when the window ends: when it was ended early, or as planned.
func (m Maintenance) End() time.Time {
	if !m.Ended.IsZero() && m.Ended.Before(m.EndsAt) {
		return m.Ended
	}
	return m.EndsAt
}

// PutMaintenance saves a maintenance window and forgets those that ended before forget.
func (s *Store) PutMaintenance(m Maintenance, forget time.Time) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx kv.Tx) error {
		var old [][]byte
		tx.Range(bucketMaintenance, kv.Range{}, func(k, v []byte) bool {
			var x Maintenance
			if json.Unmarshal(v, &x) == nil && x.End().Before(forget) {
				old = append(old, append([]byte(nil), k...))
			}
			return true
		})
		for _, k := range old {
			if err := tx.Delete(bucketMaintenance, k); err != nil {
				return err
			}
		}
		return tx.Put(bucketMaintenance, []byte(m.ID), b)
	})
}

// MaintenanceWindows lists the maintenance windows kept, the latest to start first.
func (s *Store) MaintenanceWindows() []Maintenance {
	var out []Maintenance
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketMaintenance, kv.Range{}, func(_, v []byte) bool {
			var m Maintenance
			if json.Unmarshal(v, &m) == nil {
				out = append(out, m)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(a, b int) bool {
		if !out[a].StartsAt.Equal(out[b].StartsAt) {
			return out[a].StartsAt.After(out[b].StartsAt)
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// MaintenanceWindow returns the maintenance window with id.
func (s *Store) MaintenanceWindow(id string) (Maintenance, bool) {
	var m Maintenance
	found := false
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketMaintenance, []byte(id)); v != nil {
			found = json.Unmarshal(v, &m) == nil
		}
		return nil
	})
	return m, found
}
