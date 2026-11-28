package store

import (
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// Issued reports whether secret is a credential Beacon issued, whether or not its holder may act now: an
// agent's token whose owner is disabled is still one, and so is an OAuth access token on the machines'
// gateway, which takes only tokens. A token secret that was revoked, replaced by enroll -rotate or a
// renewal, or has expired still is for RetiredFor (retired.go): its holder may not have swapped it yet.
// So is an OAuth access or refresh token whose sign-in ended (revoked, ended by a new password or with its
// agent) or that expired: its client may still be retrying it. The lockouts do not count such a secret as
// a guess; it is refused all the same.
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
	case strings.HasPrefix(secret, AccessPrefix), strings.HasPrefix(secret, RefreshPrefix):
		h, now, ok := hashToken(secret), s.now(), false
		_ = s.db.View(func(tx kv.Tx) error {
			ok = issuedOAuth(tx, secret, h, now)
			return nil
		})
		return ok
	}
	return false
}

// issuedOAuth reports whether the OAuth token secret, whose hash is h, is one Beacon issued that is live,
// or that ended or expired within RetiredFor.
func issuedOAuth(tx kv.Tx, secret, h string, now time.Time) bool {
	if v := tx.Get(bucketRetired, []byte(h)); v != nil {
		if at, err := time.Parse(time.RFC3339Nano, string(v)); err == nil && now.Before(at) {
			return true
		}
	}
	if strings.HasPrefix(secret, AccessPrefix) {
		var a accessRecord
		return decodeKey(tx, bucketAccess, []byte(h), &a) && now.Before(a.Expires.Add(RetiredFor))
	}
	return len(grants(tx, func(g Grant) bool {
		return (g.Refresh == h || slices.Contains(g.Spent, h)) && now.Before(g.Expires.Add(RetiredFor))
	})) > 0
}
