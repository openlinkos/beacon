package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The console's sign-ins: a person pastes a Beacon token into the console (package web), and gets a
// session bound to that token, kept here. Only the SHA-256 of a session's id is stored, as for tokens,
// so that the database alone opens no session. The bucket is made with the first sign-in.

var bucketConsole = []byte("console_sessions")

// ConsoleSession is one sign-in to the console. Token and TokenCreated name the token it is bound to:
// the session lasts while that very token does (a token revoked, or replaced by another of the same
// name, ends it); what the session may do is what the token's scopes allow, read afresh each time. CSRF
// is the secret every form of the session carries.
type ConsoleSession struct {
	Hash         string    `json:"hash"`
	Token        string    `json:"token"`
	TokenCreated time.Time `json:"token_created"`
	Created      time.Time `json:"created"`
	Expires      time.Time `json:"expires"`
	CSRF         string    `json:"csrf"`
}

// ErrNoSession means the console session is unknown or has ended.
var ErrNoSession = errors.New("no such console session")

// randomID is n random bytes in base64url without padding.
func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashSession(id string) string {
	sum := sha256.Sum256([]byte("console:" + id))
	return hex.EncodeToString(sum[:])
}

// CreateConsoleSession signs t in to the console for ttl, or until t expires if that comes first, and
// returns the session's id, which is not stored and goes into the person's cookie. Sessions that ended
// are forgotten on the way.
func (s *Store) CreateConsoleSession(t Token, ttl time.Duration) (string, ConsoleSession, error) {
	id, err := randomID(32)
	if err != nil {
		return "", ConsoleSession{}, err
	}
	csrf, err := randomID(32)
	if err != nil {
		return "", ConsoleSession{}, err
	}
	now := s.now().UTC()
	cs := ConsoleSession{Hash: hashSession(id), Token: t.Name, TokenCreated: t.Created, Created: now, Expires: now.Add(ttl), CSRF: csrf}
	if t.Expires.Before(cs.Expires) {
		cs.Expires = t.Expires.UTC()
	}
	b, err := json.Marshal(cs)
	if err != nil {
		return "", ConsoleSession{}, err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists(bucketConsole)
		if err != nil {
			return err
		}
		if err := deleteConsole(bk, func(x ConsoleSession) bool { return !now.Before(x.Expires) }); err != nil {
			return err
		}
		return bk.Put([]byte(cs.Hash), b)
	})
	if err != nil {
		return "", ConsoleSession{}, err
	}
	return id, cs, nil
}

// ConsoleSessionByID returns the session whose id this is, if it has not ended. Whether its token still
// holds is the caller's to check.
func (s *Store) ConsoleSessionByID(id string) (ConsoleSession, error) {
	if id == "" {
		return ConsoleSession{}, ErrNoSession
	}
	var cs ConsoleSession
	found := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		if bk := tx.Bucket(bucketConsole); bk != nil {
			if v := bk.Get([]byte(hashSession(id))); v != nil {
				found = json.Unmarshal(v, &cs) == nil
			}
		}
		return nil
	})
	if !found || !s.now().Before(cs.Expires) {
		return ConsoleSession{}, ErrNoSession
	}
	return cs, nil
}

// DeleteConsoleSession ends the session whose id this is; an unknown one is no error.
func (s *Store) DeleteConsoleSession(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(bucketConsole)
		if bk == nil {
			return nil
		}
		return bk.Delete([]byte(hashSession(id)))
	})
}

// DeleteConsoleSessions ends every session of the token called name and reports how many there were.
func (s *Store) DeleteConsoleSessions(name string) int {
	n := 0
	_ = s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(bucketConsole)
		if bk == nil {
			return nil
		}
		return deleteConsole(bk, func(x ConsoleSession) bool {
			if x.Token == name {
				n++
				return true
			}
			return false
		})
	})
	return n
}

// deleteConsole removes the sessions for which drop returns true.
func deleteConsole(bk *bolt.Bucket, drop func(ConsoleSession) bool) error {
	var keys [][]byte
	_ = bk.ForEach(func(k, v []byte) error {
		var x ConsoleSession
		if json.Unmarshal(v, &x) != nil || drop(x) {
			keys = append(keys, append([]byte(nil), k...))
		}
		return nil
	})
	for _, k := range keys {
		if err := bk.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// EachSession calls fn with the records of SSH sessions, the newest first, until fn returns false; the
// console's lists walk them this way rather than loading them all.
func (s *Store) EachSession(fn func(SessionRecord) bool) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketSessions).Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var r SessionRecord
			if json.Unmarshal(v, &r) != nil {
				continue
			}
			if !fn(r) {
				return nil
			}
		}
		return nil
	})
}
