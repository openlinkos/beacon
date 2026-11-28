package store

import (
	"encoding/json"

	"github.com/openlinkos/beacon/internal/kv"
)

// RecheckTokens makes every token found in the in-memory cache be read again from the database
// before it is accepted, while recheck reports true. A standby of a primary/standby pair sets it: its
// cache is refreshed only on a schedule, and the primary may have revoked, renewed or changed a token
// since. A token that the database no longer holds under the same secret is refused, as is any token
// when the database cannot be read. Nil turns the check off.
func (s *Store) RecheckTokens(recheck func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recheck = recheck
}

// rechecked is the database's own record of the cached token t, found by the secret's hash h, or false
// when it has gone, no longer has that secret, or cannot be read.
func (s *Store) rechecked(t Token, h string) (Token, bool) {
	var fresh Token
	found := false
	err := s.db.View(func(tx kv.Tx) error {
		v := tx.Get(bucketTokens, []byte(t.Name))
		if v == nil {
			return nil
		}
		if err := json.Unmarshal(v, &fresh); err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return Token{}, false
	}
	if fresh.Hash != h && (fresh.OldHash != h || !s.now().Before(fresh.OldUntil)) {
		return Token{}, false
	}
	if fresh.Lifetime == 0 { // from before the lifetime was kept
		fresh.Lifetime = fresh.Expires.Sub(fresh.Created)
	}
	return fresh, true
}
