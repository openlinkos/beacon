package kv

import (
	"bytes"
	"io"
	"time"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

// BoltOptions opens a bbolt file to read, for the migration into a SQL database. The file is
// opened read-only, so the migration cannot change what it copies; a backup of it is taken with
// Tx.WriteTo before anything else happens (see the store's migration).
type BoltOptions struct {
	Path    string
	Timeout time.Duration // how long to wait for the file lock; default 2s
}

// Bolt is a kv.Reader over a bbolt file.
type Bolt struct {
	db *bolt.DB
}

// OpenBolt opens the bbolt file at o.Path for reading.
func OpenBolt(o BoltOptions) (*Bolt, error) {
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Second
	}
	db, err := bolt.Open(o.Path, 0o400, &bolt.Options{Timeout: o.Timeout, ReadOnly: true})
	if err != nil {
		if err == berrors.ErrTimeout { //nolint:errorlint // bbolt returns sentinel values directly
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Bolt{db: db}, nil
}

// Buckets lists the top-level bucket names in key order.
func (b *Bolt) Buckets() [][]byte {
	var out [][]byte
	_ = b.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			out = append(out, bytes.Clone(name))
			return nil
		})
	})
	return out
}

// View runs fn in a read-only transaction. The fn must not keep what it is handed: bbolt reuses
// the buffers once the transaction ends.
func (b *Bolt) View(fn func(ROTx) error) error {
	return b.db.View(func(tx *bolt.Tx) error { return fn(&boltTx{tx: tx}) })
}

// WriteTo writes a consistent copy of the whole database (bbolt's Tx.WriteTo): the migration's
// backup, taken before anything else happens.
func (b *Bolt) WriteTo(w io.Writer) (int64, error) {
	var n int64
	err := b.db.View(func(tx *bolt.Tx) error {
		var err error
		n, err = tx.WriteTo(w)
		return err
	})
	return n, err
}

// Close closes the file.
func (b *Bolt) Close() error { return b.db.Close() }

type boltTx struct{ tx *bolt.Tx }

func (t *boltTx) bucket(name []byte) *bolt.Bucket { return t.tx.Bucket(name) }

func (t *boltTx) Get(bucket, key []byte) []byte {
	if b := t.bucket(bucket); b != nil {
		return b.Get(key)
	}
	return nil
}

func (t *boltTx) Range(bucket []byte, r Range, fn func(k, v []byte) bool) {
	b := t.bucket(bucket)
	if b == nil {
		return
	}
	c := b.Cursor()
	step := c.Next
	var k, v []byte
	if r.Reverse {
		step = c.Prev
		if len(r.Start) > 0 {
			if at, _ := c.Seek(r.Start); at == nil {
				k, v = c.Last() // nothing at or after Start: begin at the end
			} else {
				k, v = c.Prev() // the last key before Start
			}
		} else {
			k, v = c.Last()
		}
	} else if len(r.Start) > 0 {
		k, v = c.Seek(r.Start) // the first key at or after Start
	} else {
		k, v = c.First()
	}
	for ; k != nil; k, v = step() {
		if len(r.Prefix) > 0 && !bytes.HasPrefix(k, r.Prefix) {
			return
		}
		if !fn(k, v) {
			return
		}
	}
}

func (t *boltTx) Count(bucket []byte) int {
	if b := t.bucket(bucket); b != nil {
		return b.Stats().KeyN
	}
	return 0
}

func (t *boltTx) Sequence(bucket []byte) uint64 {
	if b := t.bucket(bucket); b != nil {
		return b.Sequence()
	}
	return 0
}
