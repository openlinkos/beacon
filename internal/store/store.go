// Package store keeps the service's state in one bbolt file: alert groups (card, acknowledgement,
// silence, escalation progress), API tokens (only their SHA-256), the machines the SSH channel reaches,
// the index of SSH sessions, the audit log and small counters.
// A single process holds the file lock, which also guarantees a single Feishu connection.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

var (
	bucketGroups   = []byte("groups")
	bucketTokens   = []byte("tokens")
	bucketAudit    = []byte("audit")
	bucketMeta     = []byte("meta")
	bucketSilences = []byte("silences")
	bucketMachines = []byte("machines")
	bucketSessions = []byte("sessions")
)

// ErrLocked means another process holds the database (the service is running).
var ErrLocked = errors.New("another process has the database open")

// ErrNotFound is returned for an unknown token or group.
var ErrNotFound = errors.New("not found")

// Group is one alert group and what was done about it.
type Group struct {
	ID          string            `json:"id"`
	Key         string            `json:"key"`
	Status      string            `json:"status"` // firing or resolved
	Severity    string            `json:"severity"`
	Labels      map[string]string `json:"labels"`
	Common      map[string]string `json:"common"`
	Annotations map[string]string `json:"annotations"`
	Alerts      []Alert           `json:"alerts"`
	StartsAt    time.Time         `json:"starts_at"`
	ResolvedAt  time.Time         `json:"resolved_at,omitzero"`
	Channel     string            `json:"channel"` // app, webhook or pushover (when the card could not be sent)
	MessageID   string            `json:"message_id,omitempty"`
	NotifiedAt  time.Time         `json:"notified_at,omitzero"` // the escalation clock starts here
	CardAt      time.Time         `json:"card_at,omitzero"`
	CardHash    string            `json:"card_hash,omitempty"`
	Ack         *Ack              `json:"ack,omitempty"`
	Silence     *Silence          `json:"silence,omitempty"`
	Buzzed      time.Time         `json:"buzzed,omitzero"`
	Pushed      time.Time         `json:"pushed,omitzero"`
	Receipt     string            `json:"receipt,omitempty"`
	ReceiptAt   time.Time         `json:"receipt_at,omitzero"`
	Barked      time.Time         `json:"barked,omitzero"`
	Notes       []Note            `json:"notes,omitempty"`  // see agents.go
	Agents      []AgentCall       `json:"agents,omitempty"` // the hooks called to wake agents
	Updated     time.Time         `json:"updated"`
}

// Alert is one alert in a group.
type Alert struct {
	Fingerprint  string            `json:"fingerprint"`
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"starts_at"`
	GeneratorURL string            `json:"generator_url,omitempty"`
}

// Ack records who acknowledged a group, how and when.
type Ack struct {
	By   string    `json:"by"`
	Via  string    `json:"via"` // feishu, pushover, api, mcp
	Note string    `json:"note,omitempty"`
	At   time.Time `json:"at"`
}

// Silence is the silence created for a group.
type Silence struct {
	ID    string    `json:"id"`
	By    string    `json:"by"`
	Until time.Time `json:"until"`
}

// Firing reports whether the group is firing.
func (g *Group) Firing() bool { return g.Status == "firing" }

// Token is an API token. Only the SHA-256 of the secret is kept. Subject is who a machine token speaks
// for: the probe location of a probe:write token, the host of a host:write token, the sender of a
// heartbeat:write token.
//
// SSHKeys are the public keys its holder signs in to the SSH channel with, and Machines the machines it
// may reach there (none: all of them).
type Token struct {
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	Scopes    []string  `json:"scopes"`
	Subject   string    `json:"subject,omitempty"`
	Created   time.Time `json:"created"`
	CreatedBy string    `json:"created_by,omitempty"`
	Expires   time.Time `json:"expires"`
	SSHKeys   []SSHKey  `json:"ssh_keys,omitempty"`
	Machines  []string  `json:"machines,omitempty"`
}

// SSHKey is a public key in the authorized_keys form (without its comment) and its SHA256 fingerprint.
type SSHKey struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
}

// Reaches reports whether the token may reach machine through the SSH channel.
func (t Token) Reaches(machine string) bool {
	return t.Has("machines:ssh") && (len(t.Machines) == 0 || slices.Contains(t.Machines, machine))
}

// Has reports whether the token carries scope.
func (t Token) Has(scope string) bool { return slices.Contains(t.Scopes, scope) }

// AuditEntry is one write made through the API, MCP, a card or the command line.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Via    string    `json:"via"`
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Store is the bbolt-backed state.
type Store struct {
	db  *bolt.DB
	now func() time.Time

	mu     sync.RWMutex
	tokens map[string]Token // by hash
}

const maxAudit = 10000

// Open opens or creates the database at path; it fails after 2 seconds if another process holds it.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if errors.Is(err, berrors.ErrTimeout) {
		return nil, fmt.Errorf("open %s: %w", path, ErrLocked)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &Store{db: db, now: time.Now, tokens: map[string]Token{}}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketGroups, bucketTokens, bucketAudit, bucketMeta, bucketSilences, bucketMachines, bucketSessions} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return tx.Bucket(bucketTokens).ForEach(func(_, v []byte) error {
			var t Token
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			s.tokens[t.Hash] = t
			return nil
		})
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// ---- Groups

// Group returns the group with id.
func (s *Store) Group(id string) (Group, bool) {
	var g Group
	var ok bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketGroups).Get([]byte(id)); v != nil {
			ok = json.Unmarshal(v, &g) == nil
		}
		return nil
	})
	return g, ok
}

// PutGroup saves g.
func (s *Store) PutGroup(g Group) error {
	g.Updated = s.now().UTC()
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketGroups).Put([]byte(g.ID), b) })
}

// Groups returns every group, firing ones first, then the newest first.
func (s *Store) Groups() []Group {
	var out []Group
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketGroups).ForEach(func(_, v []byte) error {
			var g Group
			if json.Unmarshal(v, &g) == nil {
				out = append(out, g)
			}
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Firing() != out[j].Firing() {
			return out[i].Firing()
		}
		return out[i].StartsAt.After(out[j].StartsAt)
	})
	return out
}

// DeleteGroups removes the groups for which drop returns true and reports how many.
func (s *Store) DeleteGroups(drop func(Group) bool) int {
	n := 0
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketGroups)
		var keys [][]byte
		_ = b.ForEach(func(k, v []byte) error {
			var g Group
			if json.Unmarshal(v, &g) == nil && drop(g) {
				keys = append(keys, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n
}

// ---- Tokens

// Scopes are the token scopes. Public reads need no token. The first seven are for people and agents
// (the REST API, MCP and the SSH channel); the rest are for machines, on the metrics gateway.
var Scopes = []string{"status:read", "alerts:write", "incidents:write", "maintenance:write",
	"machines:ssh", "machines:write", "sessions:read",
	"probe:write", "host:write", "heartbeat:write", "metrics:read", "alerts:send", "changes:write"}

// SubjectScopes need a subject: the gateway labels everything written with it.
var SubjectScopes = []string{"probe:write", "host:write"}

var (
	tokenNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	subjectRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// TokenPrefix starts every token, so a leaked one is easy to recognise and to search for.
const TokenPrefix = "lbt_"

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateToken makes a token and returns its secret, which is not stored and cannot be shown again.
func (s *Store) CreateToken(name string, scopes []string, ttl time.Duration, by string) (string, Token, error) {
	return s.CreateSubjectToken(name, "", scopes, ttl, by)
}

// CreateSubjectToken is CreateToken for a token that speaks for subject (see Token).
func (s *Store) CreateSubjectToken(name, subject string, scopes []string, ttl time.Duration, by string) (string, Token, error) {
	if !tokenNameRe.MatchString(name) {
		return "", Token{}, fmt.Errorf("token name %q: use lower case letters, digits, dots, dashes and underscores", name)
	}
	if len(scopes) == 0 {
		return "", Token{}, errors.New("a token needs at least one scope")
	}
	for _, sc := range scopes {
		if !slices.Contains(Scopes, sc) {
			return "", Token{}, fmt.Errorf("unknown scope %q; scopes are %s", sc, strings.Join(Scopes, ", "))
		}
		if slices.Contains(SubjectScopes, sc) && subject == "" {
			return "", Token{}, fmt.Errorf("scope %s needs a subject (the location or host the token writes as)", sc)
		}
	}
	if subject != "" && !subjectRe.MatchString(subject) {
		return "", Token{}, fmt.Errorf("subject %q: use lower case letters, digits and dashes", subject)
	}
	if ttl < time.Hour || ttl > 2*366*24*time.Hour {
		return "", Token{}, errors.New("a token must expire between 1 hour and 2 years from now")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, err
	}
	secret := TokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	now := s.now().UTC()
	t := Token{Name: name, Hash: hashToken(secret), Scopes: slices.Clone(scopes), Subject: subject, Created: now, CreatedBy: by, Expires: now.Add(ttl)}
	b, _ := json.Marshal(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(bucketTokens)
		if bk.Get([]byte(name)) != nil {
			return fmt.Errorf("a token named %q exists; revoke it first", name)
		}
		return bk.Put([]byte(name), b)
	})
	if err != nil {
		return "", Token{}, err
	}
	s.tokens[t.Hash] = t
	return secret, t, nil
}

// Tokens lists the tokens by name.
func (s *Store) Tokens() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RevokeToken deletes the token called name.
func (s *Store) RevokeToken(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hash string
	for h, t := range s.tokens {
		if t.Name == name {
			hash = h
		}
	}
	if hash == "" {
		return ErrNotFound
	}
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketTokens).Delete([]byte(name)) }); err != nil {
		return err
	}
	delete(s.tokens, hash)
	return nil
}

// UpdateToken changes the token called name with change, which cannot alter its name, hash or expiry.
func (s *Store) UpdateToken(name string, change func(*Token) error) (Token, error) {
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
		return Token{}, ErrNotFound
	}
	hash, expires := t.Hash, t.Expires
	if err := change(&t); err != nil {
		return Token{}, err
	}
	t.Name, t.Hash, t.Expires = name, hash, expires
	b, _ := json.Marshal(t)
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketTokens).Put([]byte(name), b) }); err != nil {
		return Token{}, err
	}
	s.tokens[hash] = t
	return t, nil
}

// TokenBySSHKey returns the unexpired token holding the key with this SHA256 fingerprint.
func (s *Store) TokenBySSHKey(fingerprint string) (Token, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tokens {
		for _, k := range t.SSHKeys {
			if k.Fingerprint == fingerprint && s.now().Before(t.Expires) {
				return t, true
			}
		}
	}
	return Token{}, false
}

// VerifyToken returns the token whose secret this is, if it exists and has not expired.
func (s *Store) VerifyToken(secret string) (Token, bool) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return Token{}, false
	}
	s.mu.RLock()
	t, ok := s.tokens[hashToken(secret)]
	s.mu.RUnlock()
	if !ok || !s.now().Before(t.Expires) {
		return Token{}, false
	}
	return t, true
}

// ---- Silences

// Matcher matches a label: equal (or, with IsEqual false, not equal) to Value, or matching it as a regular
// expression (anchored) when IsRegex.
type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// SilenceRecord is a silence: alerts matching every matcher are not delivered between StartsAt and EndsAt.
type SilenceRecord struct {
	ID        string    `json:"id"`
	Matchers  []Matcher `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
}

// keepSilences is how long an ended silence stays listed.
const keepSilences = 7 * 24 * time.Hour

// PutSilence stores a silence (new or changed) and forgets silences that ended long ago.
func (s *Store) PutSilence(r SilenceRecord) error {
	b, _ := json.Marshal(r)
	cutoff := s.now().Add(-keepSilences)
	return s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(bucketSilences)
		var old [][]byte
		_ = bk.ForEach(func(k, v []byte) error {
			var x SilenceRecord
			if json.Unmarshal(v, &x) == nil && x.EndsAt.Before(cutoff) {
				old = append(old, k)
			}
			return nil
		})
		for _, k := range old {
			_ = bk.Delete(k)
		}
		return bk.Put([]byte(r.ID), b)
	})
}

// SilenceRecords lists the stored silences, the newest first.
func (s *Store) SilenceRecords() []SilenceRecord {
	var out []SilenceRecord
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSilences).ForEach(func(_, v []byte) error {
			var r SilenceRecord
			if json.Unmarshal(v, &r) == nil {
				out = append(out, r)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.After(out[j].StartsAt) })
	return out
}

// ---- Machines

// Machine is a machine the SSH channel reaches. Address is host:port; User is the account there, which
// is also the principal of the certificates Beacon signs; HostKey is the key pinned when the machine
// was added, in the authorized_keys form.
type Machine struct {
	Name     string    `json:"name"`
	Address  string    `json:"address"`
	User     string    `json:"user"`
	HostKey  string    `json:"host_key"`
	Critical bool      `json:"critical,omitempty"`
	Note     string    `json:"note,omitempty"`
	Added    time.Time `json:"added"`
	AddedBy  string    `json:"added_by,omitempty"`
}

// MachineName is what a machine may be called: its host name, as probe locations are.
var MachineName = subjectRe

// PutMachine adds or replaces a machine.
func (s *Store) PutMachine(m Machine) error {
	if !MachineName.MatchString(m.Name) {
		return fmt.Errorf("machine name %q: use lower case letters, digits and dashes", m.Name)
	}
	b, _ := json.Marshal(m)
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketMachines).Put([]byte(m.Name), b) })
}

// Machine returns the machine called name.
func (s *Store) Machine(name string) (Machine, bool) {
	var m Machine
	var ok bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketMachines).Get([]byte(name)); v != nil {
			ok = json.Unmarshal(v, &m) == nil
		}
		return nil
	})
	return m, ok
}

// Machines lists the machines by name.
func (s *Store) Machines() []Machine {
	var out []Machine
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMachines).ForEach(func(_, v []byte) error {
			var m Machine
			if json.Unmarshal(v, &m) == nil {
				out = append(out, m)
			}
			return nil
		})
	})
	return out
}

// DeleteMachine removes the machine called name.
func (s *Store) DeleteMachine(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMachines)
		if b.Get([]byte(name)) == nil {
			return ErrNotFound
		}
		return b.Delete([]byte(name))
	})
}

// ---- SSH sessions

// SessionRecord indexes one session on a machine: a command, a shell, a file transfer or a forwarded
// port, through the SSH channel or a tool. Its recording and transcript are files; Recording names them
// relative to the recordings directory, without the extension.
type SessionRecord struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Via       string    `json:"via"` // ssh, mcp or api
	Machine   string    `json:"machine"`
	Kind      string    `json:"kind"` // exec, shell, sftp, forward or file
	Command   string    `json:"command,omitempty"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitzero"`
	Exit      *int      `json:"exit,omitempty"`
	Commands  int       `json:"commands,omitempty"`
	BytesIn   int64     `json:"bytes_in"`
	BytesOut  int64     `json:"bytes_out"`
	Recording string    `json:"recording,omitempty"`
}

// PutSession stores a session record. IDs begin with the start time, so they sort by it.
func (s *Store) PutSession(r SessionRecord) error {
	b, _ := json.Marshal(r)
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketSessions).Put([]byte(r.ID), b) })
}

// Session returns the session with id.
func (s *Store) Session(id string) (SessionRecord, bool) {
	var r SessionRecord
	var ok bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketSessions).Get([]byte(id)); v != nil {
			ok = json.Unmarshal(v, &r) == nil
		}
		return nil
	})
	return r, ok
}

// Sessions returns up to limit sessions for which keep returns true (nil keeps all), the newest first.
func (s *Store) Sessions(limit int, keep func(SessionRecord) bool) []SessionRecord {
	var out []SessionRecord
	_ = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketSessions).Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			var r SessionRecord
			if json.Unmarshal(v, &r) == nil && (keep == nil || keep(r)) {
				out = append(out, r)
			}
		}
		return nil
	})
	return out
}

// DeleteSessionsBefore removes the records of sessions that started before t and returns them.
func (s *Store) DeleteSessionsBefore(t time.Time) []SessionRecord {
	var gone []SessionRecord
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		var keys [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var r SessionRecord
			if err := json.Unmarshal(v, &r); err != nil {
				continue // not ours to judge; keep it
			}
			if !r.Started.Before(t) {
				break // keys sort by start time
			}
			gone = append(gone, r)
			keys = append(keys, append([]byte(nil), k...))
		}
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return gone
}

// ---- Audit

// Audit records an entry; the oldest entries beyond 10,000 are dropped.
func (s *Store) Audit(e AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = s.now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(bucketAudit)
		seq, _ := bk.NextSequence()
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, seq)
		if err := bk.Put(k, b); err != nil {
			return err
		}
		if n := bk.Stats().KeyN; n > maxAudit {
			c := bk.Cursor()
			for k, _ := c.First(); k != nil && n > maxAudit; k, _ = c.Next() {
				if err := c.Delete(); err != nil {
					return err
				}
				n--
			}
		}
		return nil
	})
}

// AuditLog returns the latest entries, newest first.
func (s *Store) AuditLog(limit int) []AuditEntry {
	var out []AuditEntry
	_ = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketAudit).Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			var e AuditEntry
			if json.Unmarshal(v, &e) == nil {
				out = append(out, e)
			}
		}
		return nil
	})
	return out
}

// ---- Counters and settings

// Add adds n to the counter key and returns the new value.
func (s *Store) Add(key string, n int64) int64 {
	var v int64
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if cur := b.Get([]byte(key)); len(cur) == 8 {
			v = int64(binary.BigEndian.Uint64(cur))
		}
		v += n
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v))
		return b.Put([]byte(key), buf)
	})
	return v
}

// Counter returns the counter key.
func (s *Store) Counter(key string) int64 { return s.Add(key, 0) }

// SetTime stores a time under key; the zero time deletes it.
func (s *Store) SetTime(key string, t time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if t.IsZero() {
			return b.Delete([]byte("t:" + key))
		}
		return b.Put([]byte("t:"+key), []byte(t.UTC().Format(time.RFC3339Nano)))
	})
}

// Time returns the time stored under key, or the zero time.
func (s *Store) Time(key string) time.Time {
	var t time.Time
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketMeta).Get([]byte("t:" + key)); v != nil {
			t, _ = time.Parse(time.RFC3339Nano, string(v))
		}
		return nil
	})
	return t
}
