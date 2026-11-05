// Package store keeps the service's state in one SQL database (a single SQLite file by default):
// alert groups (card, acknowledgement, silence, escalation progress), people's accounts (an
// argon2id hash of each password) and their agents, tokens and OAuth grants (only their SHA-256),
// the machines the SSH channel reaches, the index of SSH sessions, small counters — all in a
// key-value space (internal/kv) with the buckets and keys they always had — and the audit log in
// event tables with a hash chain (internal/events).
// A single process holds the lock file beside the database, which also guarantees a single Feishu
// connection.
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

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

// The buckets, with the names they have had since the bbolt file; the migration copies them as
// they are.
var (
	bucketGroups   = []byte("groups")
	bucketTokens   = []byte("tokens")
	bucketAudit    = []byte("audit") // the events tables replaced it; kept for the migration to read
	bucketMeta     = []byte("meta")
	bucketSilences = []byte("silences")
	bucketMachines = []byte("machines")
	bucketSessions = []byte("sessions")
)

// Buckets lists the bucket names the store keeps, for the migration's count check.
var Buckets = [][]byte{bucketGroups, bucketTokens, bucketMeta, bucketSilences, bucketMachines,
	bucketSessions, bucketAccounts, bucketLinks, bucketAgents, bucketClients, bucketGrants,
	bucketAccess, bucketChanges, bucketConfig, bucketConfigChanges, bucketConsole,
	bucketIncidents, bucketMaintenance}

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
//
// Lifetime is how long it was made to last; a renewal (renew.go) gives it a new secret that expires
// Lifetime from now, and the old secret (OldHash) stays valid until OldUntil, so that a client can swap
// its token file without a race. Tokens from before Lifetime was kept get Expires - Created when loaded.
//
// Agent names the agent (principals.go) whose credential the token is: then it has no scopes, machines
// or keys of its own, and may do what the agent may.
type Token struct {
	Name      string        `json:"name"`
	Hash      string        `json:"hash"`
	Scopes    []string      `json:"scopes"`
	Subject   string        `json:"subject,omitempty"`
	Created   time.Time     `json:"created"`
	CreatedBy string        `json:"created_by,omitempty"`
	Expires   time.Time     `json:"expires"`
	SSHKeys   []SSHKey      `json:"ssh_keys,omitempty"`
	Machines  []string      `json:"machines,omitempty"`
	Lifetime  time.Duration `json:"lifetime,omitempty"`
	Renewed   time.Time     `json:"renewed,omitzero"`
	OldHash   string        `json:"old_hash,omitempty"`
	OldUntil  time.Time     `json:"old_until,omitzero"`
	Agent     string        `json:"agent,omitempty"`
}

// SSHKey is a public key in the authorized_keys form (without its comment) and its SHA256 fingerprint;
// Comment is what whoever added it said it is, and Added when.
type SSHKey struct {
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	Comment     string    `json:"comment,omitempty"`
	Added       time.Time `json:"added,omitzero"`
	// Via is the credential an agent added its own key with (Principal.Cred): the key works while that
	// credential does, and goes with it. None when a person or the command line added it.
	Via string `json:"via,omitempty"`
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

// Store is the state in the SQL database.
type Store struct {
	db    *kv.SQLite
	audit *events.Log
	uses  *events.Log
	now   func() time.Time

	mu     sync.RWMutex
	tokens map[string]Token // by hash
}

// Options is how the database is opened. The zero options beside Path are the plan's defaults:
// WAL, synchronous FULL, checkpoints every 24 hours.
type Options struct {
	// Path is the database file.
	Path string
	// JournalMode and Synchronous are SQLite's pragmas; the defaults (WAL, FULL) are what a
	// deployment should keep.
	JournalMode, Synchronous string
	// BusyTimeout is how long a SQLite statement waits for a lock; 15s by default.
	BusyTimeout time.Duration
	// CheckpointInterval is how often the audit chain's checkpoints are written; 24h by default.
	CheckpointInterval time.Duration
	// Retention is how long audit records are kept; Verify refuses a prune still inside it.
	Retention time.Duration
	// SignKey signs the checkpoints (nil: one is generated and kept in the database; the key
	// hierarchy that comes with the vault will pass its own).
	SignKey []byte
}

// Open opens or creates the database at path; it fails with ErrLocked when another process holds
// it.
func Open(path string) (*Store, error) {
	return OpenWith(Options{Path: path})
}

// OpenWith is Open with the options named.
func OpenWith(o Options) (*Store, error) {
	if o.Path == "" {
		return nil, errors.New("no database path")
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil {
		return nil, err
	}
	db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: o.Path, JournalMode: o.JournalMode, Synchronous: o.Synchronous, BusyTimeout: o.BusyTimeout})
	if err != nil {
		if errors.Is(err, kv.ErrLocked) {
			return nil, fmt.Errorf("open %s: %w", o.Path, ErrLocked)
		}
		return nil, fmt.Errorf("open %s: %w", o.Path, err)
	}
	s := &Store{db: db, now: time.Now, tokens: map[string]Token{}}
	if err := s.openLogs(o); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = db.View(func(tx kv.Tx) error {
		tx.Range(bucketTokens, kv.Range{}, func(_, v []byte) bool {
			var t Token
			if json.Unmarshal(v, &t) == nil {
				if t.Lifetime == 0 { // from before the lifetime was kept
					t.Lifetime = t.Expires.Sub(t.Created)
				}
				s.tokens[t.Hash] = t
			}
			return true
		})
		return nil
	})
	return s, nil
}

// OpenRead opens the database for reading only (the CLI's checks while the service runs); it
// fails when the file is not there, and every write fails.
func OpenRead(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: path, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &Store{db: db, now: time.Now, tokens: map[string]Token{}}
	if err := s.openLogs(Options{}); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = db.View(func(tx kv.Tx) error {
		tx.Range(bucketTokens, kv.Range{}, func(_, v []byte) bool {
			var t Token
			if json.Unmarshal(v, &t) == nil {
				if t.Lifetime == 0 {
					t.Lifetime = t.Expires.Sub(t.Created)
				}
				s.tokens[t.Hash] = t
			}
			return true
		})
		return nil
	})
	return s, nil
}

func (s *Store) openLogs(o Options) error {
	eo := events.Options{SignKey: o.SignKey, CheckpointInterval: o.CheckpointInterval, Retention: o.Retention, Now: s.now}
	audit, err := events.Open(s.db.Handle(), s.db.WriteHandle(), "audit_events", eo)
	if err != nil {
		return err
	}
	uses, err := events.Open(s.db.Handle(), s.db.WriteHandle(), "secret_uses", eo)
	if err != nil {
		return err
	}
	s.audit, s.uses = audit, uses
	return nil
}

// Storage is the SQL database under the store, for the checks the command line runs.
func (s *Store) Storage() *kv.SQLite { return s.db }

// Close closes the database, folding the WAL into the file.
func (s *Store) Close() error { return s.db.Close() }

// SetClock sets the store's clock, which stamps what it writes; a nil clock is the real one.
// Tests that hold an environment's time still give the store that environment's clock, so what
// it stamps matches the rest.
func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	s.now = now
	s.audit.SetClock(now)
	s.uses.SetClock(now)
}

// ---- Groups

// Group returns the group with id.
func (s *Store) Group(id string) (Group, bool) {
	var g Group
	var ok bool
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketGroups, []byte(id)); v != nil {
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
	return s.db.Update(func(tx kv.Tx) error { return tx.Put(bucketGroups, []byte(g.ID), b) })
}

// Groups returns every group, firing ones first, then the newest first.
func (s *Store) Groups() []Group {
	var out []Group
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketGroups, kv.Range{}, func(_, v []byte) bool {
			var g Group
			if json.Unmarshal(v, &g) == nil {
				out = append(out, g)
			}
			return true
		})
		return nil
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
	_ = s.db.Update(func(tx kv.Tx) error {
		var keys [][]byte
		tx.Range(bucketGroups, kv.Range{}, func(k, v []byte) bool {
			var g Group
			if json.Unmarshal(v, &g) == nil && drop(g) {
				keys = append(keys, bytesClone(k))
			}
			return true
		})
		for _, k := range keys {
			if err := tx.Delete(bucketGroups, k); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n
}

// ---- Tokens

// Scopes are the token scopes. Public reads need no token. The first nine are for people and agents
// (the REST API, MCP and the SSH channel: PeopleScopes); the rest are for machines, on the metrics gateway.
var Scopes = []string{"status:read", "alerts:write", "incidents:write", "maintenance:write",
	"machines:ssh", "machines:write", "sessions:read", "config:write", "accounts:write",
	"probe:write", "host:write", "heartbeat:write", "metrics:read", "alerts:send", "changes:write"}

// SubjectScopes need a subject: the gateway labels everything written with it.
var SubjectScopes = []string{"probe:write", "host:write"}

var (
	tokenNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	subjectRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// TokenPrefix starts every token, the mark of a Beacon token, so a leaked one is easy to recognise and to
// search for. It outlived the old command names on purpose: tokens already issued keep working.
const TokenPrefix = "lbt_"

// secretBytes is the random part of a secret: 32 bytes (256 bits), 52 base32 characters after the
// prefix, 56 in all.
const secretBytes = 32

// newSecret makes a secret: the prefix, then the random bytes in lower-case base32 without padding.
func newSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return TokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

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
	secret, err := newSecret()
	if err != nil {
		return "", Token{}, err
	}
	now := s.now().UTC()
	t := Token{Name: name, Hash: hashToken(secret), Scopes: slices.Clone(scopes), Subject: subject, Created: now, CreatedBy: by, Expires: now.Add(ttl), Lifetime: ttl}
	b, _ := json.Marshal(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.Update(func(tx kv.Tx) error {
		if tx.Get(bucketTokens, []byte(name)) != nil {
			return fmt.Errorf("a token named %q exists; revoke it first", name)
		}
		return tx.Put(bucketTokens, []byte(name), b)
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
	err := s.db.Update(func(tx kv.Tx) error {
		if err := tx.Delete(bucketTokens, []byte(name)); err != nil {
			return err
		}
		return dropKeys(tx, "token:"+name) // the keys an agent added with this token go with it
	})
	if err != nil {
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
	if err := s.db.Update(func(tx kv.Tx) error { return tx.Put(bucketTokens, []byte(name), b) }); err != nil {
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

// VerifyToken returns the token whose secret this is, if it exists and has not expired. The secret a
// renewal replaced is still accepted during its grace period (renew.go).
func (s *Store) VerifyToken(secret string) (Token, bool) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return Token{}, false
	}
	h := hashToken(secret)
	s.mu.RLock()
	t, ok := s.tokens[h]
	if !ok {
		t, ok = s.byOldHash(h)
	}
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
	return s.db.Update(func(tx kv.Tx) error {
		var old [][]byte
		tx.Range(bucketSilences, kv.Range{}, func(k, v []byte) bool {
			var x SilenceRecord
			if json.Unmarshal(v, &x) == nil && x.EndsAt.Before(cutoff) {
				old = append(old, bytesClone(k))
			}
			return true
		})
		for _, k := range old {
			_ = tx.Delete(bucketSilences, k)
		}
		return tx.Put(bucketSilences, []byte(r.ID), b)
	})
}

// SilenceRecords lists the stored silences, the newest first.
func (s *Store) SilenceRecords() []SilenceRecord {
	var out []SilenceRecord
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketSilences, kv.Range{}, func(_, v []byte) bool {
			var r SilenceRecord
			if json.Unmarshal(v, &r) == nil {
				out = append(out, r)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.After(out[j].StartsAt) })
	return out
}

// ---- Machines

// Machine is a machine the SSH channel reaches. Address is host:port, or ConnectorAddress for a machine
// that connects out to the channel itself (see connector.go); User is the account there, which is also
// the principal of the certificates Beacon signs; HostKey is the key pinned when the machine was added,
// in the authorized_keys form (empty for a connector machine whose key is not learned yet).
// ConnectorKey is the public key a connector machine signs in with, and ConnectorFingerprint its SHA256
// fingerprint.
type Machine struct {
	Name                 string    `json:"name"`
	Address              string    `json:"address"`
	User                 string    `json:"user"`
	HostKey              string    `json:"host_key"`
	Critical             bool      `json:"critical,omitempty"`
	Note                 string    `json:"note,omitempty"`
	Added                time.Time `json:"added"`
	AddedBy              string    `json:"added_by,omitempty"`
	ConnectorKey         string    `json:"connector_key,omitempty"`
	ConnectorFingerprint string    `json:"connector_fingerprint,omitempty"`
}

// MachineName is what a machine may be called: its host name, as probe locations are.
var MachineName = subjectRe

// PutMachine adds or replaces a machine.
func (s *Store) PutMachine(m Machine) error {
	if !MachineName.MatchString(m.Name) {
		return fmt.Errorf("machine name %q: use lower case letters, digits and dashes", m.Name)
	}
	b, _ := json.Marshal(m)
	return s.db.Update(func(tx kv.Tx) error { return tx.Put(bucketMachines, []byte(m.Name), b) })
}

// Machine returns the machine called name.
func (s *Store) Machine(name string) (Machine, bool) {
	var m Machine
	var ok bool
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketMachines, []byte(name)); v != nil {
			ok = json.Unmarshal(v, &m) == nil
		}
		return nil
	})
	return m, ok
}

// Machines lists the machines by name.
func (s *Store) Machines() []Machine {
	var out []Machine
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketMachines, kv.Range{}, func(_, v []byte) bool {
			var m Machine
			if json.Unmarshal(v, &m) == nil {
				out = append(out, m)
			}
			return true
		})
		return nil
	})
	return out
}

// DeleteMachine removes the machine called name.
func (s *Store) DeleteMachine(name string) error {
	return s.db.Update(func(tx kv.Tx) error {
		if tx.Get(bucketMachines, []byte(name)) == nil {
			return ErrNotFound
		}
		return tx.Delete(bucketMachines, []byte(name))
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
	return s.db.Update(func(tx kv.Tx) error { return tx.Put(bucketSessions, []byte(r.ID), b) })
}

// Session returns the session with id.
func (s *Store) Session(id string) (SessionRecord, bool) {
	var r SessionRecord
	var ok bool
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketSessions, []byte(id)); v != nil {
			ok = json.Unmarshal(v, &r) == nil
		}
		return nil
	})
	return r, ok
}

// Sessions returns up to limit sessions for which keep returns true (nil keeps all), the newest first.
func (s *Store) Sessions(limit int, keep func(SessionRecord) bool) []SessionRecord {
	var out []SessionRecord
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketSessions, kv.Range{Reverse: true}, func(_, v []byte) bool {
			if len(out) >= limit {
				return false
			}
			var r SessionRecord
			if json.Unmarshal(v, &r) == nil && (keep == nil || keep(r)) {
				out = append(out, r)
			}
			return true
		})
		return nil
	})
	return out
}

// DeleteSessionsBefore removes the records of sessions that started before t and returns them.
func (s *Store) DeleteSessionsBefore(t time.Time) []SessionRecord {
	var gone []SessionRecord
	_ = s.db.Update(func(tx kv.Tx) error {
		var keys [][]byte
		tx.Range(bucketSessions, kv.Range{}, func(k, v []byte) bool {
			var r SessionRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return true // not ours to judge; keep it
			}
			if !r.Started.Before(t) {
				return false // keys sort by start time
			}
			gone = append(gone, r)
			keys = append(keys, bytesClone(k))
			return true
		})
		for _, k := range keys {
			if err := tx.Delete(bucketSessions, k); err != nil {
				return err
			}
		}
		return nil
	})
	return gone
}

// ---- Audit

// Audit records an entry; the oldest beyond the retention are pruned by PruneAudit, which the
// service runs on a schedule (the old cap of 10,000 is gone).
func (s *Store) Audit(e AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = s.now().UTC()
	}
	_, err := s.audit.Append(events.Event{
		Time: e.Time, Actor: e.Actor, Via: e.Via, Action: e.Action, Object: e.Target, Detail: e.Detail,
	})
	return err
}

// AuditLog returns the latest entries, newest first.
func (s *Store) AuditLog(limit int) []AuditEntry {
	if limit <= 0 {
		limit = 100
	}
	out, _, _ := s.AuditQuery(AuditFilter{Limit: limit})
	return out
}

// AuditFilter narrows an AuditQuery: empty strings and zero times leave that part open.
type AuditFilter struct {
	Actor, Action, Target string
	From, To              time.Time
	Limit, Offset         int
}

// AuditQuery returns the entries matching f, newest first, and how many match in all.
func (s *Store) AuditQuery(f AuditFilter) ([]AuditEntry, int, error) {
	got, total, err := s.audit.Query(events.Filter{
		Actor: f.Actor, Action: f.Action, Object: f.Target, From: f.From, To: f.To,
		Limit: f.Limit, Offset: f.Offset,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]AuditEntry, 0, len(got))
	for _, e := range got {
		out = append(out, AuditEntry{Time: e.Time, Actor: e.Actor, Via: e.Via, Action: e.Action, Target: e.Object, Detail: e.Detail})
	}
	return out, total, nil
}

// Result is what a chain verification found (events.Result): how much it walked, where it got
// to, and — with an error — where the chain broke.
type Result = events.Result

// VerifyAudit walks the audit chain from the checkpoint that anchors the records still present
// and checks every hash and checkpoint signature; PruneAudit keeps that possible.
func (s *Store) VerifyAudit() (events.Result, error) { return s.audit.Verify() }

// VerifyAuditSince verifies from the checkpoint at seq onwards.
func (s *Store) VerifyAuditSince(seq uint64) (events.Result, error) { return s.audit.VerifySince(seq) }

// AuditCheckpoints lists the audit chain's checkpoints, oldest first.
func (s *Store) AuditCheckpoints() ([]events.Checkpoint, error) { return s.audit.Checkpoints() }

// CheckpointAudit writes a checkpoint of the audit chain now (one is written on the interval as
// records arrive, and before the backup takes its copy).
func (s *Store) CheckpointAudit() (events.Checkpoint, error) { return s.audit.Checkpoint() }

// PruneAudit removes the audit entries older than retention, keeping the chain verifiable, and
// reports how many went.
func (s *Store) PruneAudit(retention time.Duration) (int, error) { return s.audit.Prune(retention) }

// UsesLog is the event log the secret uses will go in (the vault's packages); the table and its
// chain are made here so the storage is one piece.
func (s *Store) UsesLog() *events.Log { return s.uses }

// ---- Counters and settings

// Add adds n to the counter key and returns the new value.
func (s *Store) Add(key string, n int64) int64 {
	var v int64
	_ = s.db.Update(func(tx kv.Tx) error {
		if cur := tx.Get(bucketMeta, []byte(key)); len(cur) == 8 {
			v = int64(binary.BigEndian.Uint64(cur))
		}
		v += n
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v))
		return tx.Put(bucketMeta, []byte(key), buf)
	})
	return v
}

// Counter returns the counter key.
func (s *Store) Counter(key string) int64 { return s.Add(key, 0) }

// SetTime stores a time under key; the zero time deletes it.
func (s *Store) SetTime(key string, t time.Time) error {
	return s.db.Update(func(tx kv.Tx) error {
		if t.IsZero() {
			return tx.Delete(bucketMeta, []byte("t:"+key))
		}
		return tx.Put(bucketMeta, []byte("t:"+key), []byte(t.UTC().Format(time.RFC3339Nano)))
	})
}

// Time returns the time stored under key, or the zero time.
func (s *Store) Time(key string) time.Time {
	var t time.Time
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketMeta, []byte("t:"+key)); v != nil {
			t, _ = time.Parse(time.RFC3339Nano, string(v))
		}
		return nil
	})
	return t
}

// bytesClone keeps a key a Range walk handed out, for use after the walk.
func bytesClone(b []byte) []byte { return append([]byte(nil), b...) }
