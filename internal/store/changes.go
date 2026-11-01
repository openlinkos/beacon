package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bucketChanges is made when the first change arrives, so a database from an older version needs nothing.
var bucketChanges = []byte("changes")

// Change is an event that changed something: a deployment, a Flux reconciliation, a configuration
// change. Senders post them to the gateway, and what they say (source, kind, summary and the rest) is
// theirs; PostedBy (the token that posted it) and Received are Beacon's. IDs begin with the event's time,
// so they sort by it.
type Change struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Source   string    `json:"source"` // what sent it: flux, a CI system, a script
	Kind     string    `json:"kind"`   // deploy, config, rollback; Flux's reason, such as ReconciliationSucceeded
	Service  string    `json:"service,omitempty"`
	Machine  string    `json:"machine,omitempty"`
	Summary  string    `json:"summary"`
	URL      string    `json:"url,omitempty"`
	Revision string    `json:"revision,omitempty"`
	Object   string    `json:"object,omitempty"`   // what changed, for Flux Kind/namespace/name
	Severity string    `json:"severity,omitempty"` // Flux's: info or error
	PostedBy string    `json:"posted_by"`
	Received time.Time `json:"received"`
}

// maxChanges bounds the bucket whatever the senders do; the oldest go first.
const maxChanges = 50000

const changeTime = "20060102T150405.000000000"

// changeKey is the start of the keys of changes at t; keys sort by time.
func changeKey(t time.Time) string { return t.UTC().Format(changeTime) }

// PutChange stores a change, giving it an id, and forgets the changes that happened before forget.
func (s *Store) PutChange(c Change, forget time.Time) (Change, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return Change{}, err
	}
	c.ID = changeKey(c.Time) + "-" + hex.EncodeToString(raw)
	b, err := json.Marshal(c)
	if err != nil {
		return Change{}, err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists(bucketChanges)
		if err != nil {
			return err
		}
		if err := bk.Put([]byte(c.ID), b); err != nil {
			return err
		}
		cut := changeKey(forget)
		n := bk.Stats().KeyN
		var old [][]byte
		cur := bk.Cursor()
		for k, _ := cur.First(); k != nil && (string(k) < cut || n > maxChanges); k, _ = cur.Next() {
			old = append(old, append([]byte(nil), k...))
			n--
		}
		for _, k := range old {
			if err := bk.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Change{}, err
	}
	return c, nil
}

// ChangeFilter narrows a list of changes. Zero times leave that end open; empty strings match anything.
type ChangeFilter struct {
	From, To                 time.Time
	Service, Machine, Source string
	Limit                    int
}

// Changes returns the changes that happened between f.From and f.To and match f, the newest first.
func (s *Store) Changes(f ChangeFilter) []Change {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	var out []Change
	_ = s.db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket(bucketChanges)
		if bk == nil {
			return nil
		}
		cur := bk.Cursor()
		var k, v []byte
		if after, _ := cur.Seek([]byte(changeKey(f.To.Add(time.Nanosecond)))); f.To.IsZero() || after == nil {
			k, v = cur.Last()
		} else {
			k, v = cur.Prev() // the last change at or before f.To
		}
		from := ""
		if !f.From.IsZero() {
			from = changeKey(f.From)
		}
		for ; k != nil && len(out) < f.Limit && string(k) >= from; k, v = cur.Prev() {
			var c Change
			if json.Unmarshal(v, &c) != nil {
				continue
			}
			if (f.Service == "" || c.Service == f.Service) && (f.Machine == "" || c.Machine == f.Machine) && (f.Source == "" || c.Source == f.Source) {
				out = append(out, c)
			}
		}
		return nil
	})
	return out
}

// SessionsBetween returns up to limit sessions that were open at some time between from and to, the
// oldest first, for which keep returns true (nil keeps all). Sessions are looked for from a day before
// from: one open longer than that is missed.
func (s *Store) SessionsBetween(from, to time.Time, limit int, keep func(SessionRecord) bool) []SessionRecord {
	var out []SessionRecord
	_ = s.db.View(func(tx *bolt.Tx) error {
		cur := tx.Bucket(bucketSessions).Cursor()
		start := []byte(from.Add(-24 * time.Hour).UTC().Format("20060102-150405"))
		for k, v := cur.Seek(start); k != nil && len(out) < limit; k, v = cur.Next() {
			var r SessionRecord
			if json.Unmarshal(v, &r) != nil {
				continue
			}
			if r.Started.After(to) {
				break // keys sort by start time
			}
			if (r.Ended.IsZero() || !r.Ended.Before(from)) && (keep == nil || keep(r)) {
				out = append(out, r)
			}
		}
		return nil
	})
	return out
}
