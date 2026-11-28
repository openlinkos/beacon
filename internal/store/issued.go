package store

import (
	"strings"

	"github.com/openlinkos/beacon/internal/kv"
)

// Issued reports whether secret is a credential Beacon issued, whether or not its holder may act now: an
// agent's token whose owner is disabled is still one, and so is an OAuth access token on the machines'
// gateway, which takes only tokens. A token secret that was revoked (enroll -rotate revokes the old one),
// replaced by a renewal or has expired still is for RetiredFor (retired.go): its holder may not have
// swapped it yet. The lockouts do not count such a secret as a guess; it is refused all the same.
func (s *Store) Issued(secret string) bool {
	switch {
	case strings.HasPrefix(secret, TokenPrefix):
		if _, ok := s.VerifyToken(secret); ok {
			return true
		}
		h := hashToken(secret)
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.retiredToken(h)
	case strings.HasPrefix(secret, AccessPrefix):
		h, now, ok := hashToken(secret), s.now(), false
		_ = s.db.View(func(tx kv.Tx) error {
			var a accessRecord
			ok = decodeKey(tx, bucketAccess, []byte(h), &a) && now.Before(a.Expires)
			return nil
		})
		return ok
	}
	return false
}
