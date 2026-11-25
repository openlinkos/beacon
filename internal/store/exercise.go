package store

import (
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/exercise"
	"github.com/openlinkos/beacon/internal/kv"
)

var (
	drillTimeKey = []byte("t:drill") // keep the original deadline for earlier Beacon versions
	drillRunKey  = []byte("drill_run")
	bucketDrill  = []byte("drill_observations")
)

const maxDrillLocations = 64
const maxDrillSamples = 64 // per location, per run

func exerciseRun(tx kv.Tx) exercise.Run {
	var run exercise.Run
	var until time.Time
	if value := tx.Get(bucketMeta, drillTimeKey); len(value) > 0 {
		until, _ = time.Parse(time.RFC3339Nano, string(value))
	}
	if raw := tx.Get(bucketMeta, drillRunKey); json.Unmarshal(raw, &run) == nil &&
		(run.Ended.IsZero() && run.Until.Equal(until) || !run.Ended.IsZero() && until.IsZero()) {
		return run
	}
	return exercise.Run{Until: until} // an old arm has no source proof
}

// DrillRun returns the current or most recently ended drill.
func (s *Store) DrillRun() exercise.Run {
	var run exercise.Run
	if err := s.db.View(func(tx kv.Tx) error { run = exerciseRun(tx); return nil }); err != nil {
		return exercise.Run{}
	}
	return run
}

// SetDrill applies the legacy endpoint state, the generated run and the operator audit atomically.
func (s *Store) SetDrill(now, until time.Time, by, detail string) (exercise.Run, bool, error) {
	id := ""
	if !until.IsZero() {
		var err error
		id, err = randomID(18)
		if err != nil {
			return exercise.Run{}, false, err
		}
	}
	var run exercise.Run
	var wasActive bool
	err := s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		run = exerciseRun(tx)
		wasActive = run.Active(now)
		if until.IsZero() {
			if run.ID != "" && run.Ended.IsZero() {
				run.Ended = now.UTC()
			}
			if err := tx.Delete(bucketMeta, drillTimeKey); err != nil {
				return err
			}
		} else {
			run = exercise.Run{ID: id, Armed: now.UTC(), Until: until.UTC()}
			if err := tx.Put(bucketMeta, drillTimeKey, []byte(run.Until.Format(time.RFC3339Nano))); err != nil {
				return err
			}
			var keys [][]byte
			tx.Range(bucketDrill, kv.Range{}, func(key, _ []byte) bool {
				keys = append(keys, bytesClone(key))
				return true
			})
			for _, key := range keys {
				if err := tx.Delete(bucketDrill, key); err != nil {
					return err
				}
			}
		}
		b, err := json.Marshal(run)
		if err != nil {
			return err
		}
		if err := tx.Put(bucketMeta, drillRunKey, b); err != nil {
			return err
		}
		return s.AppendAuditTx(raw, events.Event{Time: now.UTC(), Actor: by, Via: "cli", Action: "drill", Object: run.ID, Detail: detail})
	})
	return run, wasActive, err
}

// RecordDrillObservation only admits validated proof for this run and deduplicates a replay.
func (s *Store) RecordDrillObservation(o exercise.Observation) error {
	return s.db.Update(func(tx kv.Tx) error {
		run := exerciseRun(tx)
		if run.ID == "" || run.ID != o.Run || o.Location == "" || o.Served.Before(run.Armed.Truncate(time.Millisecond)) ||
			!o.Served.Before(run.Until) || !run.Ended.IsZero() && o.Served.After(run.Ended) {
			return ErrNotFound
		}
		key := []byte(o.Location)
		raw := tx.Get(bucketDrill, key)
		var previous []exercise.Observation
		if raw == nil {
			if tx.Count(bucketDrill) >= maxDrillLocations {
				return ErrNotFound
			}
		} else if err := json.Unmarshal(raw, &previous); err != nil {
			return err
		}
		if slices.ContainsFunc(previous, func(existing exercise.Observation) bool { return existing.Sampled.Equal(o.Sampled) }) {
			return nil
		}
		previous = append(previous, o)
		slices.SortFunc(previous, func(a, b exercise.Observation) int { return a.Sampled.Compare(b.Sampled) })
		previous = previous[max(0, len(previous)-maxDrillSamples):]
		b, err := json.Marshal(previous)
		if err != nil {
			return err
		}
		return tx.Put(bucketDrill, key, b)
	})
}

// DrillObservedLocations returns locations with a verified response for the most recent run.
func (s *Store) DrillObservedLocations(run string) []string {
	var locations []string
	_ = s.db.View(func(tx kv.Tx) error {
		if run != "" && run == exerciseRun(tx).ID {
			tx.Range(bucketDrill, kv.Range{}, func(key, _ []byte) bool {
				locations = append(locations, string(key))
				return true
			})
		}
		return nil
	})
	return locations
}

// DrillEvidence reads the current run and its checked observations in a single snapshot.
func (s *Store) DrillEvidence() (exercise.Run, []exercise.Observation, error) {
	var run exercise.Run
	var observations []exercise.Observation
	err := s.db.View(func(tx kv.Tx) error {
		run = exerciseRun(tx)
		if run.ID == "" {
			return nil
		}
		var decodeErr error
		tx.Range(bucketDrill, kv.Range{}, func(_, raw []byte) bool {
			var location []exercise.Observation
			decodeErr = json.Unmarshal(raw, &location)
			if decodeErr != nil {
				return false
			}
			observations = append(observations, location...)
			return true
		})
		return decodeErr
	})
	return run, observations, err
}
