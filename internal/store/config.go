package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// The configuration Beacon holds (the catalogue, the probe targets and modules; see package configdb):
// its entries, by key, and every change to them, by version. The buckets are made with the first change.
var (
	bucketConfig        = []byte("config")
	bucketConfigChanges = []byte("config_changes")
)

// keepConfigChanges is how many changes are kept; the oldest beyond it are dropped. Tests lower it.
var keepConfigChanges = 10000

// ErrConflict means an entry of the configuration changed after it was read.
var ErrConflict = errors.New("the configuration changed meanwhile")

// ConfigChange is one change to the configuration: its version, who made it, when and through what,
// and every item it touched with the value before and after.
type ConfigChange struct {
	Version int64        `json:"version"`
	Time    time.Time    `json:"time"`
	Actor   string       `json:"actor"`
	Via     string       `json:"via"`               // cli, api or mcp; start or signal for files the service read itself
	Action  string       `json:"action"`            // put, delete, import or revert
	Reverts int64        `json:"reverts,omitempty"` // the version a revert undid
	Note    string       `json:"note,omitempty"`
	Items   []ConfigItem `json:"items"`
	// Unchanged is set, never stored, on the answer to a request that changed nothing; Version is then
	// the version in force.
	Unchanged bool `json:"unchanged,omitempty"`
	// Warnings are set, never stored, on the answer: what the configuration after the change names
	// that Beacon does not know (a heartbeat's machine not enrolled on the SSH channel), which does not
	// stop the change.
	Warnings []string `json:"warnings,omitempty"`
}

// ConfigItem is one item a change touched. Before is absent for an item added, After for one removed.
type ConfigItem struct {
	Kind   string          `json:"kind"`
	Key    string          `json:"key"`
	Op     string          `json:"op"` // added, changed or removed
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

// ConfigWrite sets one entry (Value nil: removes it), provided it still holds Was (nil: absent).
type ConfigWrite struct {
	Key        string
	Was, Value []byte
}

// Config returns the configuration's entries and its version: 0 when it was never changed.
// A read error is returned; the maps are empty then, and must not be taken as an empty catalogue.
func (s *Store) Config() (map[string][]byte, int64, error) {
	out := map[string][]byte{}
	var version int64
	err := s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketConfig, kv.Range{}, func(k, v []byte) bool {
			out[string(k)] = bytes.Clone(v)
			return true
		})
		version = int64(tx.Sequence(bucketConfigChanges))
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, version, nil
}

// CommitConfig applies writes and records c as the next version, all or nothing: when an entry no
// longer holds what its write expects, nothing changes and the error is ErrConflict.
func (s *Store) CommitConfig(c ConfigChange, writes []ConfigWrite) (ConfigChange, error) {
	if c.Time.IsZero() {
		c.Time = s.now().UTC()
	}
	c.Unchanged = false
	err := s.db.Update(func(tx kv.Tx) error {
		for _, w := range writes {
			cur := tx.Get(bucketConfig, []byte(w.Key))
			if (cur == nil) != (w.Was == nil) || !bytes.Equal(cur, w.Was) {
				return ErrConflict
			}
		}
		for _, w := range writes {
			var err error
			if w.Value == nil {
				err = tx.Delete(bucketConfig, []byte(w.Key))
			} else {
				err = tx.Put(bucketConfig, []byte(w.Key), w.Value)
			}
			if err != nil {
				return err
			}
		}
		seq, err := tx.NextSequence(bucketConfigChanges)
		if err != nil {
			return err
		}
		c.Version = int64(seq)
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if err := tx.Put(bucketConfigChanges, versionKey(c.Version), b); err != nil {
			return err
		}
		var old [][]byte // versions are consecutive: drop those the latest keepConfigChanges leave out
		tx.Range(bucketConfigChanges, kv.Range{}, func(k, _ []byte) bool {
			if int64(binary.BigEndian.Uint64(k)) > c.Version-int64(keepConfigChanges) {
				return false
			}
			old = append(old, bytes.Clone(k))
			return true
		})
		for _, k := range old {
			if err := tx.Delete(bucketConfigChanges, k); err != nil {
				return err
			}
		}
		return nil
	})
	return c, err
}

func versionKey(v int64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, uint64(v))
	return k
}

// ConfigChanges returns up to limit changes older than version before (0: from the newest) for which
// keep returns true (nil keeps all), the newest first.
func (s *Store) ConfigChanges(before int64, limit int, keep func(ConfigChange) bool) []ConfigChange {
	var out []ConfigChange
	_ = s.db.View(func(tx kv.Tx) error {
		// walk backwards from the newest version older than before (0: from the newest)
		r := kv.Range{Reverse: true}
		if before > 0 {
			r.Start = versionKey(before)
		}
		tx.Range(bucketConfigChanges, r, func(_, v []byte) bool {
			if len(out) >= limit {
				return false
			}
			var ch ConfigChange
			if json.Unmarshal(v, &ch) == nil && (keep == nil || keep(ch)) {
				out = append(out, ch)
			}
			return true
		})
		return nil
	})
	return out
}

// ConfigChangeAt returns the change that made version v.
func (s *Store) ConfigChangeAt(v int64) (ConfigChange, bool) {
	var ch ConfigChange
	var ok bool
	_ = s.db.View(func(tx kv.Tx) error {
		if raw := tx.Get(bucketConfigChanges, versionKey(v)); raw != nil {
			ok = json.Unmarshal(raw, &ch) == nil
		}
		return nil
	})
	return ch, ok
}
