package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
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
func (s *Store) Config() (map[string][]byte, int64) {
	out := map[string][]byte{}
	var version int64
	_ = s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(bucketConfig); b != nil {
			_ = b.ForEach(func(k, v []byte) error {
				out[string(k)] = bytes.Clone(v)
				return nil
			})
		}
		if b := tx.Bucket(bucketConfigChanges); b != nil {
			version = int64(b.Sequence())
		}
		return nil
	})
	return out, version
}

// CommitConfig applies writes and records c as the next version, all or nothing: when an entry no
// longer holds what its write expects, nothing changes and the error is ErrConflict.
func (s *Store) CommitConfig(c ConfigChange, writes []ConfigWrite) (ConfigChange, error) {
	if c.Time.IsZero() {
		c.Time = s.now().UTC()
	}
	c.Unchanged = false
	err := s.db.Update(func(tx *bolt.Tx) error {
		entries, err := tx.CreateBucketIfNotExists(bucketConfig)
		if err != nil {
			return err
		}
		changes, err := tx.CreateBucketIfNotExists(bucketConfigChanges)
		if err != nil {
			return err
		}
		for _, w := range writes {
			cur := entries.Get([]byte(w.Key))
			if (cur == nil) != (w.Was == nil) || !bytes.Equal(cur, w.Was) {
				return ErrConflict
			}
		}
		for _, w := range writes {
			if w.Value == nil {
				err = entries.Delete([]byte(w.Key))
			} else {
				err = entries.Put([]byte(w.Key), w.Value)
			}
			if err != nil {
				return err
			}
		}
		seq, err := changes.NextSequence()
		if err != nil {
			return err
		}
		c.Version = int64(seq)
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if err := changes.Put(versionKey(c.Version), b); err != nil {
			return err
		}
		var old [][]byte // versions are consecutive: drop those the latest keepConfigChanges leave out
		cur := changes.Cursor()
		for k, _ := cur.First(); k != nil && int64(binary.BigEndian.Uint64(k)) <= c.Version-int64(keepConfigChanges); k, _ = cur.Next() {
			old = append(old, bytes.Clone(k))
		}
		for _, k := range old {
			if err := changes.Delete(k); err != nil {
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
	_ = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketConfigChanges)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		var k, v []byte
		if at, _ := c.Seek(versionKey(before)); before > 0 && at != nil {
			k, v = c.Prev() // the newest older than before
		} else {
			k, v = c.Last()
		}
		for ; k != nil && len(out) < limit; k, v = c.Prev() {
			var ch ConfigChange
			if json.Unmarshal(v, &ch) == nil && (keep == nil || keep(ch)) {
				out = append(out, ch)
			}
		}
		return nil
	})
	return out
}

// ConfigChangeAt returns the change that made version v.
func (s *Store) ConfigChangeAt(v int64) (ConfigChange, bool) {
	var ch ConfigChange
	var ok bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(bucketConfigChanges); b != nil {
			if raw := b.Get(versionKey(v)); raw != nil {
				ok = json.Unmarshal(raw, &ch) == nil
			}
		}
		return nil
	})
	return ch, ok
}
