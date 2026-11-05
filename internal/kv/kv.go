// Package kv is the key-value layer under the store: everything the store kept in one bbolt file
// (buckets of JSON values, ordered keys, per-bucket sequences, counters) over a SQL database
// instead. The interface is what the store used of bbolt, no more: Get, Put, Delete, an ordered
// Range with a prefix, NextSequence, CompareAndSwap, and Update/View transactions.
//
// SQLite is the default backend (pure Go, no cgo); PostgreSQL is to follow. A bbolt file can be
// read as a migration source (Reader).
//
// Buckets are namespaces, not objects to create: reading from a bucket that was never written is
// empty, not an error. Keys and values are arbitrary bytes; keys sort in byte order, as bbolt's
// did, and Range walks them in that order (or reversed).
package kv

import "errors"

// ErrLocked means another process holds the database's lock file (the service is running).
var ErrLocked = errors.New("another process has the database open")

// ErrCompareFailed is CompareAndSwap's answer when the key does not hold the expected value.
var ErrCompareFailed = errors.New("the value changed meanwhile")

// DB is one database: a single writer at a time, any number of readers. Update runs fn in a
// read-write transaction that takes the write lock before the first statement (SQLite's
// BEGIN IMMEDIATE), View in a read-only one; either sees its own consistent snapshot. An error
// from fn rolls an Update back.
type DB interface {
	View(fn func(Tx) error) error
	Update(fn func(Tx) error) error
	Close() error
}

// Tx is one transaction. Get returns nil for a missing key; write methods fail the transaction.
// A read-only transaction's NextSequence fails: sequences advance only in an Update.
type Tx interface {
	// Get returns the value of key in bucket, or nil when the key is not there. An empty
	// value is an empty slice, not nil. A read that fails fails the transaction; Update and
	// View return that error even if fn itself did not.
	Get(bucket, key []byte) []byte
	// Put sets the value of key in bucket.
	Put(bucket, key, value []byte) error
	// Delete removes key from bucket; deleting a key that is not there is not an error.
	Delete(bucket, key []byte) error
	// Range calls fn for each key and value in bucket that has the prefix and falls inside the
	// bounds, in key order (descending when r.Reverse), until fn returns false or the walk fails.
	Range(bucket []byte, r Range, fn func(k, v []byte) bool)
	// Count is how many keys bucket holds.
	Count(bucket []byte) int
	// NextSequence returns the bucket's next sequence number, counting this call: the first is 1.
	// It is stored with the transaction, so a rolled-back Update does not consume a number.
	NextSequence(bucket []byte) (uint64, error)
	// Sequence is the number NextSequence handed out last (0 when the bucket never had one).
	Sequence(bucket []byte) uint64
	// SetSequence places the bucket's sequence where a source database had it; the migration's
	// to use, nothing else.
	SetSequence(bucket []byte, seq uint64) error
	// CompareAndSwap sets the value of key to value when it now holds old (nil: the key is absent);
	// otherwise it fails the transaction with ErrCompareFailed.
	CompareAndSwap(bucket, key, old, value []byte) error
}

// Range is where a Range walk starts and what it takes in. Zero is the whole bucket, in key
// order. With Start set, the walk begins at the first key at or after Start (ascending), or at
// the last key before Start (descending), matching a bbolt cursor's Seek followed by a step back.
// The key at Start itself is never visited in reverse walks; give the successor of the key you
// want to start at.
type Range struct {
	Prefix  []byte
	Start   []byte
	Reverse bool
}

// Reader is a read-only database: a bbolt file open for the migration into a SQL one.
type Reader interface {
	// Buckets lists the top-level bucket names in key order.
	Buckets() [][]byte
	// View runs fn in a read-only transaction.
	View(fn func(ROTx) error) error
	Close() error
}

// ROTx reads what a migration needs from the source: the buckets' contents, counts and sequences.
type ROTx interface {
	Get(bucket, key []byte) []byte
	Range(bucket []byte, r Range, fn func(k, v []byte) bool)
	Count(bucket []byte) int
	Sequence(bucket []byte) uint64
}
