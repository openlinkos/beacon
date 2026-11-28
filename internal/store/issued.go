package store

import (
	"strings"

	"github.com/openlinkos/beacon/internal/kv"
)

// Issued reports whether secret is a credential Beacon issued that has not expired or been revoked,
// whether or not its holder may act now: an agent's token whose owner is disabled is still one, and so
// is an OAuth access token on the machines' gateway, which takes only tokens. The lockouts do not count
// such a secret as a guess.
func (s *Store) Issued(secret string) bool {
	switch {
	case strings.HasPrefix(secret, TokenPrefix):
		_, ok := s.VerifyToken(secret)
		return ok
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
