package store

import (
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// Retired secrets: the hashes of tokens that were revoked (enroll -rotate revokes a machine's old token)
// or replaced by a renewal, kept for RetiredFor. A client that has not swapped its token file yet keeps
// sending the old secret; it is refused like any secret that does not work, but the lockouts do not
// count it as a guess (Issued), so its retries cannot lock out the new token at the same address.
// Nothing is accepted with a retired secret.

// bucketRetired holds the retired hashes: the key is the hash, the value the time it stops counting as
// issued (RFC 3339).
var bucketRetired = []byte("retired_tokens")

// RetiredFor is how long a revoked or replaced token secret, or an expired one, still counts as one
// Beacon issued.
const RetiredFor = 7 * 24 * time.Hour

// retire records hashes as retired until until, inside a write, and forgets the ones that have lapsed.
// The walk only collects; the deletes come after it.
func retire(tx kv.Tx, now, until time.Time, hashes ...string) error {
	var lapsed [][]byte
	tx.Range(bucketRetired, kv.Range{}, func(k, v []byte) bool {
		if at, err := time.Parse(time.RFC3339Nano, string(v)); err != nil || !now.Before(at) {
			lapsed = append(lapsed, bytesClone(k))
		}
		return true
	})
	for _, k := range lapsed {
		if err := tx.Delete(bucketRetired, k); err != nil {
			return err
		}
	}
	for _, h := range hashes {
		if h == "" {
			continue
		}
		if err := tx.Put(bucketRetired, []byte(h), []byte(until.UTC().Format(time.RFC3339Nano))); err != nil {
			return err
		}
	}
	return nil
}

// readRetired reads the retired hashes, with when each lapses; a lookup checks that it has not.
func readRetired(tx kv.Tx) map[string]time.Time {
	out := map[string]time.Time{}
	tx.Range(bucketRetired, kv.Range{}, func(k, v []byte) bool {
		if at, err := time.Parse(time.RFC3339Nano, string(v)); err == nil {
			out[string(k)] = at
		}
		return true
	})
	return out
}

// noteRetired adds hashes to the in-memory set, after the write that recorded them.
func (s *Store) noteRetired(until time.Time, hashes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for h, at := range s.retired {
		if !now.Before(at) {
			delete(s.retired, h)
		}
	}
	for _, h := range hashes {
		if h != "" {
			s.retired[h] = until
		}
	}
}

// retiredToken reports whether the token hash h is one Beacon issued that was revoked, replaced or has
// expired within RetiredFor. The caller holds s.mu.
func (s *Store) retiredToken(h string) bool {
	now := s.now()
	if at, ok := s.retired[h]; ok && now.Before(at) {
		return true
	}
	if t, ok := s.tokens[h]; ok && now.Before(t.Expires.Add(RetiredFor)) {
		return true
	}
	for _, t := range s.tokens {
		if t.OldHash == h && !t.OldUntil.IsZero() && now.Before(t.OldUntil.Add(RetiredFor)) {
			return true
		}
	}
	return false
}

// retiredHashes are the secrets of t that a client may still send when t goes: its own, and the one its
// last renewal replaced, unless that was retired longer than RetiredFor ago.
func retiredHashes(t Token, now time.Time) []string {
	out := []string{t.Hash}
	if t.OldHash != "" && now.Before(t.OldUntil.Add(RetiredFor)) {
		out = append(out, t.OldHash)
	}
	return out
}
