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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
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
	"sync/atomic"
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
	bucketIncidents, bucketMaintenance, bucketMachineRevisions, bucketOutcomes, bucketOutcomeDeliveries, bucketConsoleTargets, bucketSlackNotices, bucketPageLinkGenerations}

// ErrLocked means another process holds the database (the service is running).
var ErrLocked = errors.New("another process has the database open")

// ErrNotFound is returned for an unknown token or group.
var ErrNotFound = errors.New("not found")

// Group is one alert group and what was done about it.
type Group struct {
	ID                   string                       `json:"id"`
	Key                  string                       `json:"key"`
	Status               string                       `json:"status"` // firing or resolved
	Severity             string                       `json:"severity"`
	OpeningSeverity      string                       `json:"opening_severity,omitempty"`
	Origin               string                       `json:"origin,omitempty"`
	ExerciseRun          string                       `json:"exercise_run,omitempty"`
	Labels               map[string]string            `json:"labels"`
	Common               map[string]string            `json:"common"`
	Annotations          map[string]string            `json:"annotations"`
	Alerts               []Alert                      `json:"alerts"`
	StartsAt             time.Time                    `json:"starts_at"`
	ResolvedAt           time.Time                    `json:"resolved_at,omitzero"`
	Channel              string                       `json:"channel"`                 // the initial chat route that accepted the alert
	ChatRouteID          string                       `json:"chat_route_id,omitempty"` // the destination that accepted the initial chat notification
	ChatRecoveries       []ChatRecovery               `json:"chat_recoveries,omitempty"`
	MessageID            string                       `json:"message_id,omitempty"`
	NotifiedAt           time.Time                    `json:"notified_at,omitzero"` // the escalation clock starts here
	ChannelAt            time.Time                    `json:"channel_at,omitzero"`  // acceptance of the first chat notification
	CardAt               time.Time                    `json:"card_at,omitzero"`
	CardHash             string                       `json:"card_hash,omitempty"`
	Ack                  *Ack                         `json:"ack,omitempty"`
	PriorAcks            []Ack                        `json:"prior_acks,omitempty"`
	Silence              *Silence                     `json:"silence,omitempty"`
	Buzzed               time.Time                    `json:"buzzed,omitzero"`
	Pushed               time.Time                    `json:"pushed,omitzero"`
	Receipt              string                       `json:"receipt,omitempty"`
	ReceiptAt            time.Time                    `json:"receipt_at,omitzero"`
	Barked               time.Time                    `json:"barked,omitzero"`
	Notifications        []Notification               `json:"notifications,omitempty"`
	NotifyWebhook        bool                         `json:"notify_webhook,omitempty"`  // this episode was routed to the notification webhook
	WebhookNotices       []WebhookNotice              `json:"webhook_notices,omitempty"` // queued in incident order, including earlier episodes
	Notes                []Note                       `json:"notes,omitempty"`           // see agents.go
	Agents               []AgentCall                  `json:"agents,omitempty"`          // the hooks called to wake agents
	ProbeRuns            []ProbeRun                   `json:"probe_runs,omitempty"`
	PassingCheck         *Verification                `json:"passing_check,omitempty"` // retained independently of the recent probe runs
	Episode              string                       `json:"episode,omitempty"`
	Participation        []Participation              `json:"participation,omitempty"`
	HumanAcknowledgement *OutcomeHumanAcknowledgement `json:"human_acknowledgement,omitempty"`
	Updated              time.Time                    `json:"updated"`

	// MonitorMachines pin the designated enrolments at the start of this episode.
	MonitorMachines []MonitorMachine `json:"monitor_machines,omitempty"`

	// These track the session summary delivered on the current Feishu card and back off failed refreshes.
	CardSessionHash      string    `json:"card_session_hash,omitempty"`
	CardSessionAttemptAt time.Time `json:"card_session_attempt_at,omitzero"`

	// Receipt routing lasts as long as this emergency; pending cancellations also survive refiring.
	ReceiptRouteID        string                 `json:"receipt_route_id,omitempty"`
	ReceiptExpiresAt      time.Time              `json:"receipt_expires_at,omitzero"`
	PushoverCancelID      string                 `json:"pushover_cancel_id,omitempty"`
	PushoverCancelStatus  string                 `json:"pushover_cancel_status,omitempty"`
	PushoverCancellations []PushoverCancellation `json:"pushover_cancellations,omitempty"`

	NotifySlack  bool            `json:"-"` // stored separately from the group JSON
	SlackNotices []WebhookNotice `json:"-"`
	slackStored  bool
}

// PushoverCancellation stops retries for one accepted emergency on its original route.
type PushoverCancellation struct {
	ID        string    `json:"id"`
	Episode   string    `json:"episode,omitempty"`
	Receipt   string    `json:"receipt"`
	RouteID   string    `json:"route_id"`
	ExpiresAt time.Time `json:"expires_at"`
	RetryAt   time.Time `json:"retry_at,omitzero"`
}

// ChatRecovery holds the original Feishu destination and content for a resolved episode.
type ChatRecovery struct {
	At        time.Time       `json:"at"`
	RetryAt   time.Time       `json:"retry_at,omitzero"`
	Episode   string          `json:"episode,omitempty"`
	Channel   string          `json:"channel"`
	RouteID   string          `json:"route_id"`
	MessageID string          `json:"message_id,omitempty"`
	Card      json.RawMessage `json:"card,omitempty"`
	Text      string          `json:"text,omitempty"`
}

// WebhookNotice is a notification that has not yet been accepted by the receiver.
type WebhookNotice struct {
	Delivery string          `json:"delivery"`
	Event    string          `json:"event"`
	Episode  string          `json:"episode"`
	At       time.Time       `json:"at"`
	RetryAt  time.Time       `json:"retry_at,omitzero"`
	Body     json.RawMessage `json:"body"`
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
	Kind string    `json:"kind,omitempty"` // person, agent or unattributed; absent on older records
}

// StopsEscalation reports whether the acknowledgement pauses notifications to people.
// Standalone and older unclassified acknowledgements retain their original behaviour.
func (a *Ack) StopsEscalation() bool { return a != nil && a.Kind != "agent" }

// Notification records a provider submission outcome, independently of human acknowledgement.
type Notification struct {
	At                   time.Time `json:"at"`
	Channel              string    `json:"channel"`
	Event                string    `json:"event,omitempty"`    // resolved for a recovery update, cancel for an emergency cancellation
	Result               string    `json:"result"`             // ok or failed
	RouteID              string    `json:"route_id,omitempty"` // keyed identity of the configuration used for this attempt
	DeviceAcknowledgedAt time.Time `json:"device_acknowledged_at,omitzero"`
}

// Silence is the silence created for a group. Kind is who made it, as for an acknowledgement.
type Silence struct {
	ID    string    `json:"id"`
	By    string    `json:"by"`
	Until time.Time `json:"until"`
	Kind  string    `json:"kind,omitempty"` // person, agent or unattributed; absent on older records
}

// Pauses reports whether the silence holds at now and pauses notifications to people. An agent's
// silence never does, as an agent's acknowledgement does not.
func (s *Silence) Pauses(now time.Time) bool {
	return s != nil && s.Kind != "agent" && now.Before(s.Until)
}

// Firing reports whether the group is firing.
func (g *Group) Firing() bool { return g.Status == "firing" }

// Token is an API token. Only the SHA-256 of the secret is kept. Subject is who a machine token speaks
// for: the probe location of a probe:write token, the host of a host:write token, the heartbeat of a
// heartbeat:write token.
//
// SSHKeys are the public keys its holder signs in to the SSH channel with, and MachineAccess the machines
// it may reach there (machine_access.go): all of them that are not critical, or the ones it names.
//
// Lifetime is how long it was made to last; a renewal (renew.go) gives it a new secret that expires
// Lifetime from now, and the old secret (OldHash) stays valid until OldUntil, so that a client can swap
// its token file without a race. Tokens from before Lifetime was kept get Expires - Created when loaded.
//
// Agent names the agent (principals.go) whose credential the token is: then it has no scopes, machines
// or keys of its own, and may do what the agent may.
type Token struct {
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	Scopes    []string  `json:"scopes"`
	Subject   string    `json:"subject,omitempty"`
	Created   time.Time `json:"created"`
	CreatedBy string    `json:"created_by,omitempty"`
	Expires   time.Time `json:"expires"`
	SSHKeys   []SSHKey  `json:"ssh_keys,omitempty"`
	MachineAccess
	Lifetime time.Duration `json:"lifetime,omitempty"`
	Renewed  time.Time     `json:"renewed,omitzero"`
	OldHash  string        `json:"old_hash,omitempty"`
	OldUntil time.Time     `json:"old_until,omitzero"`
	Agent    string        `json:"agent,omitempty"`
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
	db      kv.SQL
	audit   *events.Log
	uses    *events.Log
	now     func() time.Time
	dialect events.Dialect

	mu      sync.RWMutex     // the token cache
	tokenMu sync.Mutex       // token mutations versus RefreshTokens; VerifyToken does not take it
	tokens  map[string]Token // by hash
	recheck func() bool      // read a cached token again before accepting it (recheck.go)

	auditFailed atomic.Pointer[func(AuditEntry, error)] // audit_failure.go
}

// Options is how the database is opened. The zero options beside Path (SQLite) or DSN (PostgreSQL)
// are the plan's defaults: WAL, synchronous FULL, checkpoints every 24 hours, a read pool of four
// PostgreSQL connections and a 15s statement timeout.
type Options struct {
	// Driver is "sqlite" (the default) or "postgres".
	Driver string
	// Path is the SQLite database file.
	Path string
	// JournalMode and Synchronous are SQLite's pragmas; the defaults (WAL, FULL) are what a
	// deployment should keep.
	JournalMode, Synchronous string
	// DSN is the PostgreSQL connection string, read from the file the configuration names
	// (storage.dsn_file); it never appears in logs or errors.
	DSN string
	// PoolSize is the PostgreSQL read pool's size; writes take one connection of their own. 4 by
	// default.
	PoolSize int
	// StatementTimeout bounds each PostgreSQL statement; 15s by default, zero turns it off.
	StatementTimeout time.Duration
	// BusyTimeout is how long a SQLite statement waits for a lock; 15s by default.
	BusyTimeout time.Duration
	// CheckpointInterval is how often the audit chain's checkpoints are written; 24h by default.
	CheckpointInterval time.Duration
	// Retention is how long Prune keeps audit records. Verify treats signed prune marks as the
	// authority for gaps and does not re-check them against the current retention.
	Retention time.Duration
	// SignKey signs the checkpoints (nil: one is generated and kept in the database; the key
	// hierarchy that comes with the vault will pass its own).
	SignKey []byte
}

// Open opens or creates the SQLite database at path; it fails with ErrLocked when another process
// holds it.
func Open(path string) (*Store, error) {
	return OpenWith(Options{Path: path})
}

// OpenWith opens or creates the database the options name: SQLite at Path (the default) or
// PostgreSQL at DSN.
func OpenWith(o Options) (*Store, error) {
	s, db, err := openBackend(o, false)
	if err != nil {
		return nil, err
	}
	if err := s.load(db, o); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenRead opens the database for reading only (the CLI's checks while the service runs); it
// fails when the file is not there, and every write fails. With PostgreSQL there is no file to
// hold: the database simply opens without its write pool.
func OpenRead(path string) (*Store, error) {
	return OpenReadWith(Options{Path: path})
}

// OpenReadWith is OpenRead with the options named.
func OpenReadWith(o Options) (*Store, error) {
	s, db, err := openBackend(o, true)
	if err != nil {
		return nil, err
	}
	if err := s.load(db, o); err != nil {
		return nil, err
	}
	return s, nil
}

// openBackend opens the database the options name and the store that will sit on it, with its
// event logs open (read-only logs read the tables the service made).
func openBackend(o Options, ro bool) (*Store, kv.SQL, error) {
	var db kv.SQL
	var dialect events.Dialect
	switch strings.ToLower(strings.TrimSpace(o.Driver)) {
	case "", "sqlite":
		if o.Path == "" {
			return nil, nil, errors.New("no database path")
		}
		if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil {
			return nil, nil, err
		}
		sq, err := kv.OpenSQLite(kv.SQLiteOptions{Path: o.Path, JournalMode: o.JournalMode, Synchronous: o.Synchronous, BusyTimeout: o.BusyTimeout, ReadOnly: ro})
		if err != nil {
			if errors.Is(err, kv.ErrLocked) {
				return nil, nil, fmt.Errorf("open %s: %w", o.Path, ErrLocked)
			}
			return nil, nil, fmt.Errorf("open %s: %w", o.Path, err)
		}
		db, dialect = sq, events.SQLite
	case "postgres":
		pg, err := kv.OpenPostgres(kv.PostgresOptions{DSN: o.DSN, PoolSize: o.PoolSize, StatementTimeout: o.StatementTimeout, ReadOnly: ro})
		if err != nil {
			return nil, nil, err
		}
		db, dialect = pg, events.PostgreSQL
	default:
		return nil, nil, fmt.Errorf("storage driver %q: sqlite or postgres", o.Driver)
	}
	s := &Store{db: db, now: time.Now, tokens: map[string]Token{}, dialect: dialect}
	var write *sql.DB
	if !ro {
		write = db.WriteHandle()
	}
	eo := events.Options{Dialect: dialect, SignKey: o.SignKey, CheckpointInterval: o.CheckpointInterval, Retention: o.Retention, Now: s.now}
	audit, err := events.Open(db.Handle(), write, "audit_events", eo)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	uses, err := events.Open(db.Handle(), write, "secret_uses", eo)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	s.audit, s.uses = audit, uses
	return s, db, nil
}

// load reads the tokens into memory, as every open does.
func (s *Store) load(db kv.SQL, _ Options) error {
	return db.View(func(tx kv.Tx) error {
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
}

// Storage is the SQL database under the store, for the checks the command line runs.
func (s *Store) Storage() kv.SQL { return s.db }

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

// RefreshTokens re-reads every token from the database into the in-memory cache, replacing what it
// held. The cache is what open filled: a single process owns it, but the two instances of a
// primary/standby pair share the database, and the standby's snapshot would not know the tokens
// the primary made while it held the lead — so the service refreshes when it takes over, and on a
// schedule while it waits. A failed read leaves the table it held, so a blip does not refuse every
// token.
func (s *Store) RefreshTokens() error {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	tokens := map[string]Token{}
	err := s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketTokens, kv.Range{}, func(_, v []byte) bool {
			var t Token
			if json.Unmarshal(v, &t) == nil {
				if t.Lifetime == 0 { // from before the lifetime was kept
					t.Lifetime = t.Expires.Sub(t.Created)
				}
				tokens[t.Hash] = t
			}
			return true
		})
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.tokens = tokens
	s.mu.Unlock()
	return nil
}

// SetWriteGuard runs fn at the start of every write transaction (the HA lease check). SQLite
// ignores it; PostgreSQL and the event logs take it.
func (s *Store) SetWriteGuard(fn func(context.Context, *sql.Tx) error) {
	type guarded interface {
		SetWriteGuard(func(context.Context, *sql.Tx) error)
	}
	if g, ok := s.db.(guarded); ok {
		g.SetWriteGuard(fn)
	}
	s.audit.SetGuard(fn)
	s.uses.SetGuard(fn)
}

// ---- Groups

// Group returns the group with id.
func (s *Store) Group(id string) (Group, bool) {
	var g Group
	var ok bool
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketGroups, []byte(id)); v != nil {
			ok = json.Unmarshal(v, &g) == nil
			if ok {
				g.readSlackDelivery(tx.Get(bucketSlackNotices, []byte(id)))
			}
		}
		return nil
	})
	return g, ok
}

// PutGroup saves g.
func (s *Store) PutGroup(g Group) error {
	return s.PutGroupWithOutcome(g, nil)
}

// PutGroupWithOutcome updates the alert group and its current or preceding outcome in one transaction.
func (s *Store) PutGroupWithOutcome(g Group, outcome *IncidentOutcome) error {
	return s.db.Update(func(tx kv.Tx) error {
		return s.putGroupWithOutcome(tx, g, outcome)
	})
}

func (s *Store) putGroupWithOutcome(tx kv.Tx, g Group, outcome *IncidentOutcome) error {
	if outcome != nil {
		if err := putOutcome(tx, *outcome); err != nil {
			return err
		}
	}
	return s.putGroup(tx, g)
}

func (s *Store) putGroup(tx kv.Tx, g Group) error {
	g.Updated = s.now().UTC()
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err := putSlackDelivery(tx, g); err != nil {
		return err
	}
	return tx.Put(bucketGroups, []byte(g.ID), b)
}

// PutGroupAndAudit saves a group change, its resolved outcome if present, and its audit entry.
func (s *Store) PutGroupAndAudit(g Group, outcome *IncidentOutcome, e AuditEntry) error {
	return s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		if err := s.putGroupWithOutcome(tx, g, outcome); err != nil {
			return err
		}
		return s.AppendAuditTx(raw, events.Event{
			Time: e.Time, Actor: e.Actor, Via: e.Via, Action: e.Action, Object: e.Target, Detail: e.Detail,
		})
	})
}

// Groups returns every group, firing ones first, then the newest first.
func (s *Store) Groups() []Group {
	var out []Group
	_ = s.db.View(func(tx kv.Tx) error {
		var slack map[string][]byte
		tx.Range(bucketSlackNotices, kv.Range{}, func(key, raw []byte) bool {
			if slack == nil {
				slack = make(map[string][]byte)
			}
			slack[string(key)] = bytesClone(raw)
			return true
		})
		tx.Range(bucketGroups, kv.Range{}, func(_, v []byte) bool {
			var g Group
			if json.Unmarshal(v, &g) == nil {
				g.readSlackDelivery(slack[g.ID])
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
			if err := tx.Delete(bucketSlackNotices, k); err != nil {
				return err
			}
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

// Scopes are the token scopes. PeopleScopes also work through human and delegated identities.
var Scopes = []string{"status:read", "alerts:write", "incidents:write", "maintenance:write",
	"machines:ssh", "machines:write", "sessions:read", "config:write", "accounts:write", "secrets:use", "alerts:report",
	"probe:write", "host:write", "heartbeat:write", "metrics:read", "alerts:send", "changes:write"}

// SubjectScopes need a subject: the gateway labels everything written with it, and a heartbeat token
// writes only its own heartbeat.
var SubjectScopes = []string{"probe:write", "host:write", "heartbeat:write"}

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
			return "", Token{}, fmt.Errorf("scope %s needs a subject (the location, host or heartbeat the token writes as; beacon enroll makes such tokens)", sc)
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
	t := Token{Name: name, Hash: hashToken(secret), Scopes: slices.Clone(scopes), Subject: subject, Created: now, CreatedBy: by, Expires: now.Add(ttl), Lifetime: ttl,
		MachineAccess: NamedAccess()} // no machine until it is given some
	b, _ := json.Marshal(t)
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	err = s.db.Update(func(tx kv.Tx) error {
		if tx.Get(bucketTokens, []byte(name)) != nil {
			return fmt.Errorf("a token named %q exists; revoke it first", name)
		}
		return tx.Put(bucketTokens, []byte(name), b)
	})
	if err != nil {
		return "", Token{}, err
	}
	s.mu.Lock()
	s.tokens[t.Hash] = t
	s.mu.Unlock()
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

// RevokeToken deletes the token called name, with the links it made.
func (s *Store) RevokeToken(name string) error {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	var hash string
	err := s.db.Update(func(tx kv.Tx) error {
		v := tx.Get(bucketTokens, []byte(name))
		if v == nil {
			return ErrNotFound
		}
		var t Token
		if err := json.Unmarshal(v, &t); err != nil {
			return err
		}
		hash = t.Hash
		if err := tx.Delete(bucketTokens, []byte(name)); err != nil {
			return err
		}
		// the invitations and reset links it made go with it, so that a later token of the same name
		// does not inherit them
		if err := deleteLinks(tx, func(l AccountLink) bool { return l.Maker == "token:"+name }); err != nil {
			return err
		}
		return dropKeys(tx, "token:"+name) // the keys an agent added with this token go with it
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.tokens, hash)
	s.mu.Unlock()
	return nil
}

// UpdateToken changes the token called name with change, which cannot alter its name, hash or expiry.
// The token is read from the database inside the write, so a revoke that landed on another instance
// of a pair is not written back from a stale cache.
func (s *Store) UpdateToken(name string, change func(*Token) error) (Token, error) {
	return s.updateToken(name, func(_ kv.Tx, t *Token) error { return change(t) })
}

func (s *Store) updateToken(name string, change func(kv.Tx, *Token) error) (Token, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	var t Token
	err := s.db.Update(func(tx kv.Tx) error {
		v := tx.Get(bucketTokens, []byte(name))
		if v == nil {
			return ErrNotFound
		}
		if err := json.Unmarshal(v, &t); err != nil {
			return err
		}
		if t.Lifetime == 0 {
			t.Lifetime = t.Expires.Sub(t.Created)
		}
		hash, expires := t.Hash, t.Expires
		if err := change(tx, &t); err != nil {
			return err
		}
		t.Name, t.Hash, t.Expires = name, hash, expires
		if t.Agent == "" { // an agent's token reaches what its agent does
			t.MachineAccess = t.normal()
			if err := t.check(); err != nil {
				return err
			}
		}
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		return tx.Put(bucketTokens, []byte(name), b)
	})
	if err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	s.tokens[t.Hash] = t
	s.mu.Unlock()
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
	recheck := s.recheck
	s.mu.RUnlock()
	if ok && recheck != nil && recheck() {
		t, ok = s.rechecked(t, h)
	}
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
	// ByAgent marks an agent's silence: it is kept and listed, but it mutes nothing, so that people
	// are still notified of what it matches.
	ByAgent bool `json:"byAgent,omitempty"`
}

// keepSilences is how long an ended silence stays listed.
const keepSilences = 7 * 24 * time.Hour

// PutSilence stores a silence (new or changed) and forgets silences that ended long ago.
func (s *Store) PutSilence(r SilenceRecord) error {
	b, _ := json.Marshal(r)
	cutoff := s.now().Add(-keepSilences)
	return s.db.Update(func(tx kv.Tx) error {
		if _, err := tx.NextSequence(bucketMaintenanceRevision); err != nil {
			return err
		}
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
		out = statusRecords[SilenceRecord](tx, bucketSilences)
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
	MetadataVersion      int64     `json:"metadata_version,omitempty"`
	HistoryStart         int64     `json:"history_start,omitempty"`
	LegacyHistory        bool      `json:"legacy_history,omitempty"` // history began after the enrolment
}

// MachineName is what a machine may be called: its host name, as probe locations are.
var MachineName = subjectRe

// PutMachine adds or replaces a machine.
func (s *Store) PutMachine(m Machine) error {
	return s.db.Update(func(tx kv.Tx) error {
		return s.startMachineHistory(tx, &m, "registered", "")
	})
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
func (s *Store) DeleteMachine(name string) error { return s.DeleteMachineIf(name, nil) }

// DeleteMachineIf removes the machine called name when check, given the machine as it is in the same
// transaction, says it may be: its error is returned and nothing is removed.
func (s *Store) DeleteMachineIf(name string, check func(Machine) error) error {
	return s.db.Update(func(tx kv.Tx) error {
		m, err := machineIn(tx, name)
		if err != nil {
			return err
		}
		if check != nil {
			if err := check(m); err != nil {
				return err
			}
		}
		return tx.Delete(bucketMachines, []byte(name))
	})
}

// ---- SSH sessions

// EvidenceState describes the evidence Beacon captured for a session. A missing value means the
// session predates evidence tracking.
type EvidenceState string

const (
	EvidenceCapturing    EvidenceState = "capturing"
	EvidenceComplete     EvidenceState = "complete"
	EvidenceTruncated    EvidenceState = "truncated"
	EvidenceUnavailable  EvidenceState = "unavailable"
	EvidenceMetadataOnly EvidenceState = "metadata_only"
)

// SessionRecord indexes one session on a machine: a command, a shell, a file transfer or a forwarded
// port, through the SSH channel or a tool. Its recording and transcript are files; Recording names them
// relative to the recordings directory, without the extension.
type SessionRecord struct {
	ID         string    `json:"id"`
	Agent      string    `json:"agent"`                // display name of the actor (historical field name)
	Principal  string    `json:"principal,omitempty"`  // user:NAME, agent:NAME or token:NAME
	Credential string    `json:"credential,omitempty"` // public key fingerprint, token name or grant ID
	Via        string    `json:"via"`                  // ssh, mcp or api
	Machine    string    `json:"machine"`
	Kind       string    `json:"kind"` // exec, shell, sftp, subsystem, forward or file
	Command    string    `json:"command,omitempty"`
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended,omitzero"`
	Exit       *int      `json:"exit,omitempty"`
	Commands   int       `json:"commands,omitempty"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	Recording  string    `json:"recording,omitempty"`
	// PTY says whether the session had a terminal; nil on sessions recorded before Beacon kept it.
	PTY *bool `json:"pty,omitempty"`
	// Registration is the machine's enrolment at capture. It is absent on older recordings.
	MachineRegistration string `json:"machine_registration,omitempty"`
	// Critical says the machine was critical when the session started: only those who reach critical
	// machines see it.
	Critical bool `json:"critical,omitempty"`

	RecordingState  EvidenceState `json:"recording_state,omitempty"`
	TranscriptState EvidenceState `json:"transcript_state,omitempty"`
	// Stopped says Beacon closed the session itself, and why: StoppedRecordingLimit.
	Stopped string `json:"stopped,omitempty"`
}

// StoppedRecordingLimit is the Stopped of a session closed when its recording reached its cap.
const StoppedRecordingLimit = "recording_limit"

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

// SessionsStartedBefore lists sessions that started before t, oldest first, without deleting them.
func (s *Store) SessionsStartedBefore(t time.Time) []SessionRecord {
	var out []SessionRecord
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketSessions, kv.Range{}, func(_, v []byte) bool {
			var r SessionRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return true
			}
			if !r.Started.Before(t) {
				return false
			}
			out = append(out, r)
			return true
		})
		return nil
	})
	return out
}

// DeleteSession removes the session with id.
func (s *Store) DeleteSession(id string) error {
	return s.db.Update(func(tx kv.Tx) error {
		return tx.Delete(bucketSessions, []byte(id))
	})
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
	if err != nil {
		s.reportAuditFailure(e, err)
	}
	return err
}

// AppendAuditTx appends an audit event in the same write transaction as the recorded change.
func (s *Store) AppendAuditTx(tx *sql.Tx, e events.Event) error {
	return s.audit.AppendTx(tx, &e)
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
	ExactRelatedTarget    string // include this exact target even outside the time window
	From, To              time.Time
	Limit, Offset         int
}

// AuditQuery returns the entries matching f, newest first, and how many match in all.
func (s *Store) AuditQuery(f AuditFilter) ([]AuditEntry, int, error) {
	got, total, err := s.audit.Query(events.Filter{
		Actor: f.Actor, Action: f.Action, Object: f.Target, ExactRelatedObject: f.ExactRelatedTarget, From: f.From, To: f.To,
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

// SwapString stores v under key and returns what was there before ("" for nothing).
func (s *Store) SwapString(key, v string) (string, error) {
	var old string
	err := s.db.Update(func(tx kv.Tx) error {
		old = string(tx.Get(bucketMeta, []byte("s:"+key)))
		return tx.Put(bucketMeta, []byte("s:"+key), []byte(v))
	})
	return old, err
}

// bytesClone keeps a key a Range walk handed out, for use after the walk.
func bytesClone(b []byte) []byte { return append([]byte(nil), b...) }
