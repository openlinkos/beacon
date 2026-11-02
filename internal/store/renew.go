package store

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Tokens that renew themselves: a token's holder asks for a new secret before the old one expires, so
// that nobody hands out long-lived tokens by hand. The token keeps its name, scopes, subject, machines,
// SSH keys and history; only the secret and the expiry change, and the old secret keeps working for a
// grace period, long enough for a client to swap its token file. Tokens of enrolled machines are not
// renewed this way: `enroll -rotate` replaces them.

const (
	// RenewGrace is how long the secret a renewal replaced keeps working.
	RenewGrace = 10 * time.Minute
	// RenewEvery is the least time between two renewals of one token.
	RenewEvery = 24 * time.Hour
)

var (
	// ErrTooSoon means the token was renewed less than RenewEvery ago.
	ErrTooSoon = errors.New("the token was renewed less than 24 hours ago")
	// ErrNotRenewable means the token is an enrolled machine's, replaced with enroll -rotate.
	ErrNotRenewable = errors.New("the token is an enrolled machine's and cannot renew itself; linkos-beacon enroll -rotate replaces it")
)

// RotatedScopes are the scopes of enrolled machines' tokens (linkos-beacon enroll probe|host|heartbeat),
// which are replaced with enroll -rotate rather than renewed by their holder.
var RotatedScopes = []string{"probe:write", "host:write", "heartbeat:write"}

// FormatTTL writes a lifetime the way `token create -expires` takes it (ctl.ParseTTL): whole days as
// 365d, anything else in Go's form (12h0m0s).
func FormatTTL(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	}
	return d.String()
}

// Renewable reports whether the token may renew itself: it is not an enrolled machine's.
func (t Token) Renewable() bool {
	return !slices.ContainsFunc(t.Scopes, func(s string) bool { return slices.Contains(RotatedScopes, s) })
}

// RenewableAt is when the token may next renew itself: now if it never was, else RenewEvery after the
// last renewal.
func (t Token) RenewableAt() time.Time {
	if t.Renewed.IsZero() {
		return t.Created
	}
	return t.Renewed.Add(RenewEvery)
}

// RenewToken gives the token called name a new secret, expiring its Lifetime from now, and returns the
// secret, which is not stored and cannot be shown again. The old secret stays valid for RenewGrace.
func (s *Store) RenewToken(name string) (string, Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Token
	found := false
	for _, x := range s.tokens {
		if x.Name == name {
			t, found = x, true
		}
	}
	if !found {
		return "", Token{}, ErrNotFound
	}
	if !t.Renewable() {
		return "", Token{}, ErrNotRenewable
	}
	now := s.now().UTC()
	if !t.Renewed.IsZero() && now.Before(t.Renewed.Add(RenewEvery)) {
		return "", Token{}, fmt.Errorf("%w; it may renew again at %s", ErrTooSoon, t.Renewed.Add(RenewEvery).Format(time.RFC3339))
	}
	if t.Lifetime <= 0 {
		t.Lifetime = t.Expires.Sub(t.Created)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, err
	}
	secret := TokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	old := t.Hash
	t.OldHash, t.OldUntil = old, now.Add(RenewGrace)
	t.Hash, t.Expires, t.Renewed = hashToken(secret), now.Add(t.Lifetime), now
	b, _ := json.Marshal(t)
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketTokens).Put([]byte(name), b) }); err != nil {
		return "", Token{}, err
	}
	delete(s.tokens, old)
	s.tokens[t.Hash] = t
	return secret, t, nil
}

// byOldHash finds the token whose last renewal replaced the secret with this hash, while its grace
// period lasts. The caller holds s.mu. Only a miss on the current hashes comes here, so the walk over
// the few tokens costs nothing in the common case.
func (s *Store) byOldHash(h string) (Token, bool) {
	now := s.now()
	for _, t := range s.tokens {
		if t.OldHash == h && now.Before(t.OldUntil) {
			return t, true
		}
	}
	return Token{}, false
}
