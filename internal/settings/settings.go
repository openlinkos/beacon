// Package settings keeps Beacon's system configuration in its database: every knob the secrets
// work offers (request and grant limits, the review chain, rate limits, retention, import,
// backup), each with a default value, versions, history and revert, and a signature on every row
// so that someone holding the database file alone cannot widen a limit (the design's third
// invariant). A signed head names the live rows by the hash of their stored bytes and a monotonic
// epoch; an epochs file in the data directory is the rollback anchor. A missing, stale or
// unverifiable row is served as that key's registered failsafe (never a looser default) and is
// reported to the alert chat. The head is never rebuilt by scanning the bucket except on the
// explicit RebuildHead command.
//
// Values live at one of five scopes, and the one in force is resolved level by level: system,
// collection, tag, entitlement, request — the more specific wins, except that a request may only
// tighten what the level above allows, and several tags on one item take the stricter value.
// Approval levels do not travel this line; they take the strictest, as the design says elsewhere.
//
// The signature key is a 32-byte SK passed to Open (W4 writes it to <data_dir>/settings.sk).
// W5 will call SetKey with the vault's SK. store.Secret("settings") is never a signing key.
package settings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

// The scopes a value may sit at, least specific first. Collection, tag and entitlement name their
// target (a collection's name, a tag's name, an entitlement's name); request names the request.
const (
	ScopeSystem      = "system"
	ScopeCollection  = "collection"
	ScopeTag         = "tag"
	ScopeEntitlement = "entitlement"
	ScopeRequest     = "request"
)

// level numbers the scopes for the resolution order.
var levels = map[string]int{ScopeSystem: 0, ScopeCollection: 1, ScopeTag: 2, ScopeEntitlement: 3, ScopeRequest: 4}

// Scopes lists the scopes a value may sit at, in order.
var Scopes = []string{ScopeSystem, ScopeCollection, ScopeTag, ScopeEntitlement, ScopeRequest}

// Buckets the rows and their history live in, beside the store's own.
var (
	bucketSettings        = []byte("settings")
	bucketSettingsChanges = []byte("settings_changes")
	headKey               = []byte("\x00head")
)

// Errors the API, MCP and the admin socket turn into answers.
var (
	ErrNotFound = errors.New("no such setting")
	ErrUnknown  = errors.New("unknown setting")
	ErrInvalid  = errors.New("the value does not fit this setting")
	ErrConflict = errors.New("the setting changed")
	ErrBadInput = errors.New("bad input")
	ErrHead     = errors.New("the settings head is unusable")
)

// Row is one setting as stored: the value at a scope, with its version (the number of the change
// that last touched it), who made that change and why, and the signature over all of it.
type Row struct {
	Key     string          `json:"key"`
	Scope   string          `json:"scope"`
	Target  string          `json:"target,omitempty"`
	Value   json.RawMessage `json:"value"`
	Version int64           `json:"version"`
	Actor   string          `json:"actor"`
	Via     string          `json:"via"` // cli, api, mcp or console
	At      time.Time       `json:"at"`
	Reason  string          `json:"reason,omitempty"`
	Sig     []byte          `json:"sig"`
}

// Change is one change to the settings: what a row held before and after. Reset removes the row;
// revert restores what a change had replaced.
type Change struct {
	Version  int64           `json:"version"`
	Time     time.Time       `json:"time"`
	Actor    string          `json:"actor"`
	Via      string          `json:"via"`
	Action   string          `json:"action"` // set, reset or revert
	Reverts  int64           `json:"reverts,omitempty"`
	Key      string          `json:"key"`
	Scope    string          `json:"scope"`
	Target   string          `json:"target,omitempty"`
	Before   json.RawMessage `json:"before,omitempty"`
	After    json.RawMessage `json:"after,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	PrevHash string          `json:"prev_hash,omitempty"`
	Hash     string          `json:"hash,omitempty"`
	Sig      []byte          `json:"sig,omitempty"`
	// Unchanged is set, never stored, when a change was asked for and nothing differed.
	Unchanged bool `json:"unchanged,omitempty"`
}

// Actor is who changes a setting: the token's or account's name, and how.
type Actor struct {
	Name string
	Via  string
}

// Poster sends a plain message to the alert chat.
type Poster interface {
	Send(ctx context.Context, text string) error
}

// Deps are what the settings need. Bot may be nil. Key is the 32-byte SK; empty until SetKey,
// and every Value is the registered failsafe until then. EpochsDir is the directory of the
// epochs file (the data directory).
type Deps struct {
	Store     *store.Store
	Bot       Poster
	Lang      i18n.Lang
	Log       *slog.Logger
	Now       func() time.Time
	Key       []byte
	EpochsDir string
}

// Service holds the settings: the rows in the database, verified against the signed head, and
// the defaults registered in code (Definitions). Rows that fail Accept, or a head that is
// missing, stale or unverifiable, are served as the registered failsafe, reported to the log,
// the audit log and the alert chat, and listed by Bad for the console to show. With no Key
// (the vault still sealed) every Value is the registered failsafe until SetKey.
type Service struct {
	d     Deps
	mu    sync.Mutex // serialises changes
	key   []byte     // the HMAC key
	table *signedhead.Table

	rowsMtx       sync.Mutex
	rows          map[string]Row  // by rowKey; only rows that verified and Accept
	broken        map[string]bool // live keys that did not Accept; served as failsafe
	bad           []string        // rows that did not verify, "scope/target/key"
	tableFailsafe bool            // whole table uses failsafe; Commit refuses writes
}

// Open loads the settings from the store. A store that cannot be read fails. With no Key, every
// Value is the registered failsafe until SetKey.
func Open(d Deps) (*Service, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Service{d: d}
	s.table = &signedhead.Table{
		Name:    "settings",
		Bucket:  bucketSettings,
		HeadKey: headKey,
		SK:      func() []byte { return s.key },
		Anchor:  signedhead.FileAnchor{Dir: d.EpochsDir},
		Alert:   func(msg string) { s.d.Log.Error(msg) },
	}
	if len(d.Key) == 32 {
		s.key = append([]byte(nil), d.Key...)
	} else if len(d.Key) != 0 {
		return nil, fmt.Errorf("%w: the signature key is 32 bytes", ErrBadInput)
	}
	s.warnLeftovers()
	s.reload()
	return s, nil
}

// MarkSealed drops the signature key so every Value is the registered failsafe. The vault
// wiring calls this while the vault is sealed; SetKey lifts it.
func (s *Service) MarkSealed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key = nil
	s.reload()
}

// Sealed reports whether the settings have no signature key (the vault is sealed).
func (s *Service) Sealed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.key) == 0
}

// reload reads the signed head and every row. Only rows the head names whose stored bytes hash
// as it records, and whose HMAC holds, are served. A named row that is missing or bad is
// failsafe for that key. A missing, bad or stale head makes the whole table failsafe.
func (s *Service) reload() {
	if len(s.key) == 0 {
		s.rowsMtx.Lock()
		s.rows, s.broken, s.bad, s.tableFailsafe = map[string]Row{}, map[string]bool{}, nil, false
		s.rowsMtx.Unlock()
		return
	}
	rows := map[string]Row{}
	broken := map[string]bool{}
	var bad []string
	var tableFailsafe bool
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		res := s.table.Load(tx)
		if res.Status == signedhead.StatusFailsafe {
			tableFailsafe = true
			return nil
		}
		tx.Range(bucketSettings, kv.Range{}, func(k, v []byte) bool {
			if string(k) == string(headKey) {
				return true
			}
			rk := string(k)
			if !signedhead.Accept(res, rk, v) {
				if _, named := res.Head.Entries[rk]; named {
					broken[rk] = true
					bad = append(bad, labelFromRowKey(rk))
				} else {
					bad = append(bad, labelFromRowKey(rk)+" (not in head)")
				}
				return true
			}
			var r Row
			if err := json.Unmarshal(v, &r); err != nil || !hmac.Equal(r.Sig, sign(s.key, r)) {
				broken[rk] = true
				bad = append(bad, labelFromRowKey(rk))
				return true
			}
			rows[rk] = r
			return true
		})
		for rk := range res.Head.Entries {
			if _, ok := rows[rk]; ok {
				continue
			}
			if broken[rk] {
				continue
			}
			broken[rk] = true
			bad = append(bad, labelFromRowKey(rk))
		}
		return nil
	})
	ub := unique(bad)
	s.rowsMtx.Lock()
	s.rows, s.broken, s.bad, s.tableFailsafe = rows, broken, ub, tableFailsafe
	s.rowsMtx.Unlock()
	if tableFailsafe {
		s.d.Log.Error("the settings head is unusable; every setting is served as its failsafe value")
		_ = s.d.Store.Audit(store.AuditEntry{Actor: "beacon", Via: "system", Action: "settings_head_failsafe",
			Detail: "the settings head is unusable; every setting is served as its failsafe value"})
		s.alertHead()
		return
	}
	for _, b := range ub {
		s.d.Log.Error("a settings row failed its signature and is served as its failsafe value", "row", b)
	}
	if len(ub) > 0 {
		_ = s.d.Store.Audit(store.AuditEntry{Actor: "beacon", Via: "system", Action: "settings_signature_invalid",
			Detail: fmt.Sprintf("%d row(s) failed their signature and are served as their failsafe values", len(ub))})
		s.alertBroken(ub)
	}
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (s *Service) alertBroken(rows []string) {
	if s.d.Bot == nil {
		return
	}
	_ = s.d.Bot.Send(context.Background(), i18n.T(s.d.Lang, "Settings rows failed their signature and are served as their failsafe values: %s.", strings.Join(rows, ", ")))
}

func (s *Service) alertHead() {
	if s.d.Bot == nil {
		return
	}
	_ = s.d.Bot.Send(context.Background(), i18n.T(s.d.Lang, "The settings head is unusable; every setting is served as its failsafe value."))
}

func (s *Service) warnLeftovers() {
	var leftover bool
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		if tx.Get([]byte("settings_meta"), []byte("mac-migrated")) != nil {
			leftover = true
		}
		if tx.Get([]byte("meta"), []byte("secret:settings")) != nil {
			leftover = true
		}
		return nil
	})
	if !leftover {
		return
	}
	s.d.Log.Error("ignored a leftover settings signing secret in the database; it is not a signing key")
	_ = s.d.Store.Audit(store.AuditEntry{Actor: "beacon", Via: "system", Action: "settings_secret_ignored",
		Detail: "ignored a leftover settings signing secret in the database; it is not a signing key"})
	if s.d.Bot == nil {
		return
	}
	_ = s.d.Bot.Send(context.Background(), i18n.T(s.d.Lang, "Ignored a leftover settings signing secret in the database; it is not a signing key."))
}

// Bad lists the rows that failed their signature, as "scope/target/key", for the console to show.
func (s *Service) Bad() []string {
	s.rowsMtx.Lock()
	defer s.rowsMtx.Unlock()
	return slices.Clone(s.bad)
}

func (s *Service) afterHead(h signedhead.Head) {
	if err := s.table.AfterCommit(h); err != nil {
		s.d.Log.Error("the settings epochs file could not be written", "err", err)
	}
}

// Set writes value for key at scope (target names the collection, tag, entitlement or request;
// empty for the system scope), as a new version, and records the change. IfVersion, when not 0,
// is the version the caller last saw: a row that moved since is refused with ErrConflict. Reason
// is kept with the change and shown where the change is shown.
func (s *Service) Set(ctx context.Context, who Actor, scope, target, key string, value json.RawMessage, reason string, ifVersion int64) (Change, error) {
	def, ok := Definitions[key]
	if !ok {
		return Change{}, fmt.Errorf("%w: %q; settings.get lists what there is", ErrUnknown, key)
	}
	if err := checkScopeTarget(scope, target); err != nil {
		return Change{}, err
	}
	value, err := def.canonical(value)
	if err != nil {
		return Change{}, fmt.Errorf("%w: %s %q: %w", ErrInvalid, key, string(value), err)
	}
	if err := checkKeyScope(key, scope); err != nil {
		return Change{}, err
	}
	if err := requireReason(s, reason); err != nil {
		return Change{}, err
	}
	if len(s.key) == 0 {
		return Change{}, fmt.Errorf("%w: the settings have no signature key", ErrBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return Change{}, fmt.Errorf("the vault is sealed; settings cannot be changed until it is unsealed")
	}
	var out Change
	var committed signedhead.Head
	var wrote bool
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		if err := s.checkLeasePair(tx, key, value); err != nil {
			return err
		}
		cur, ok := s.row(tx, scope, target, key)
		if ifVersion != 0 && (!ok || cur.Version != ifVersion) {
			return ErrConflict
		}
		if ok && string(cur.Value) == string(value) {
			out = Change{Version: cur.Version, Time: s.d.Now().UTC(), Actor: who.Name, Via: who.Via, Action: "set",
				Key: key, Scope: scope, Target: target, Before: cur.Value, After: value, Reason: strings.TrimSpace(reason), Unchanged: true}
			return nil
		}
		version, err := tx.NextSequence(bucketSettingsChanges)
		if err != nil {
			return err
		}
		row := Row{Key: key, Scope: scope, Target: target, Value: value, Version: int64(version),
			Actor: who.Name, Via: who.Via, At: s.d.Now().UTC(), Reason: strings.TrimSpace(reason)}
		row.Sig = sign(s.key, row)
		b, err := putRow(tx, row)
		if err != nil {
			return err
		}
		var before json.RawMessage
		if ok {
			before = cur.Value
		}
		out = Change{Version: int64(version), Time: row.At, Actor: who.Name, Via: who.Via, Action: "set",
			Key: key, Scope: scope, Target: target, Before: before, After: value, Reason: row.Reason}
		if err := putChange(tx, s.key, out); err != nil {
			return err
		}
		out.Hash, out.Sig = "", nil
		h, err := s.table.Commit(tx, map[string][]byte{rowKey(scope, target, key): b}, nil)
		if err != nil {
			return headErr(err)
		}
		committed, wrote = h, true
		return nil
	})
	if err != nil {
		return Change{}, err
	}
	if wrote {
		s.afterHead(committed)
	}
	s.reload()
	s.record(ctx, who, out)
	return out, nil
}

func headErr(err error) error {
	if errors.Is(err, signedhead.ErrFailsafe) {
		return fmt.Errorf("%w: run beacon settings rebuild-head", ErrHead)
	}
	return err
}

// Reset removes the row for key at scope, so the value falls back to what the level above (or the
// default) gives, and records the change.
func (s *Service) Reset(ctx context.Context, who Actor, scope, target, key, reason string, ifVersion int64) (Change, error) {
	if _, ok := Definitions[key]; !ok {
		return Change{}, fmt.Errorf("%w: %q", ErrUnknown, key)
	}
	if err := checkScopeTarget(scope, target); err != nil {
		return Change{}, err
	}
	if err := checkKeyScope(key, scope); err != nil {
		return Change{}, err
	}
	if err := requireReason(s, reason); err != nil {
		return Change{}, err
	}
	if len(s.key) == 0 {
		return Change{}, fmt.Errorf("%w: the settings have no signature key", ErrBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Change
	var committed signedhead.Head
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		cur, ok := s.row(tx, scope, target, key)
		if !ok {
			return fmt.Errorf("%w: %s is not set at %s", ErrNotFound, key, scopeName(scope, target))
		}
		if ifVersion != 0 && cur.Version != ifVersion {
			return ErrConflict
		}
		version, err := tx.NextSequence(bucketSettingsChanges)
		if err != nil {
			return err
		}
		if err := tx.Delete(bucketSettings, rowKeyBytes(scope, target, key)); err != nil {
			return err
		}
		out = Change{Version: int64(version), Time: s.d.Now().UTC(), Actor: who.Name, Via: who.Via, Action: "reset",
			Key: key, Scope: scope, Target: target, Before: cur.Value, Reason: strings.TrimSpace(reason)}
		if err := putChange(tx, s.key, out); err != nil {
			return err
		}
		h, err := s.table.Commit(tx, nil, []string{rowKey(scope, target, key)})
		if err != nil {
			return headErr(err)
		}
		committed = h
		return nil
	})
	if err != nil {
		return Change{}, err
	}
	s.afterHead(committed)
	s.reload()
	s.record(ctx, who, out)
	return out, nil
}

// Revert undoes the change with that version, as a new change, when no later change touched the
// same row (else ErrConflict). Every action undoes the same way: what the change found in place
// goes back — a set that replaced a value gets it back, a set that made the row gets it removed,
// a reset gets its value back.
func (s *Service) Revert(ctx context.Context, who Actor, version int64, reason string) (Change, error) {
	if err := requireReason(s, reason); err != nil {
		return Change{}, err
	}
	if len(s.key) == 0 {
		return Change{}, fmt.Errorf("%w: the settings have no signature key", ErrBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Change
	var committed signedhead.Head
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		ch, ok := changeAt(tx, version)
		if !ok {
			return fmt.Errorf("%w: version %d", ErrNotFound, version)
		}
		if !ch.verifies(s.key) {
			return fmt.Errorf("%w: version %d failed its signature", ErrConflict, version)
		}
		if absent(ch.Before) && absent(ch.After) {
			return fmt.Errorf("%w: version %d changed nothing", ErrNotFound, version)
		}
		def, ok := Definitions[ch.Key]
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknown, ch.Key)
		}
		cur, ok := s.row(tx, ch.Scope, ch.Target, ch.Key)
		if ok && cur.Version > ch.Version {
			return fmt.Errorf("%w: %s at %s was changed after version %d", ErrConflict, ch.Key, scopeName(ch.Scope, ch.Target), version)
		}
		next, err := tx.NextSequence(bucketSettingsChanges)
		if err != nil {
			return err
		}
		var before json.RawMessage
		if ok {
			before = cur.Value
		}
		row := Row{Key: ch.Key, Scope: ch.Scope, Target: ch.Target, Version: int64(next),
			Actor: who.Name, Via: who.Via, At: s.d.Now().UTC(), Reason: strings.TrimSpace(reason)}
		rk := rowKey(ch.Scope, ch.Target, ch.Key)
		var put map[string][]byte
		var del []string
		if absent(ch.Before) {
			if ok {
				if err := tx.Delete(bucketSettings, rowKeyBytes(ch.Scope, ch.Target, ch.Key)); err != nil {
					return err
				}
			}
			del = []string{rk}
		} else {
			back, err := def.canonical(ch.Before)
			if err != nil {
				return fmt.Errorf("%w: the value this change would restore no longer fits %s", ErrInvalid, ch.Key)
			}
			row.Value = back
			row.Sig = sign(s.key, row)
			b, err := putRow(tx, row)
			if err != nil {
				return err
			}
			put = map[string][]byte{rk: b}
		}
		out = Change{Version: int64(next), Time: row.At, Actor: who.Name, Via: who.Via, Action: "revert",
			Reverts: version, Key: ch.Key, Scope: ch.Scope, Target: ch.Target, Before: before, After: row.Value, Reason: row.Reason}
		if err := putChange(tx, s.key, out); err != nil {
			return err
		}
		h, err := s.table.Commit(tx, put, del)
		if err != nil {
			return headErr(err)
		}
		committed = h
		return nil
	})
	if err != nil {
		return Change{}, err
	}
	s.afterHead(committed)
	s.reload()
	s.record(ctx, who, out)
	return out, nil
}

// History lists the changes, newest first, narrowed to one setting when key is not empty, and (with
// it) one scope and target. limit caps the list; 0 means 20.
func (s *Service) History(key, scope, target string, before int64, limit int) []Change {
	if limit <= 0 {
		limit = 20
	}
	out := []Change{}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		start := kv.Range{Reverse: true}
		if before > 0 {
			start.Start = versionKeyBytes(before)
		}
		tx.Range(bucketSettingsChanges, start, func(_, v []byte) bool {
			var ch Change
			if json.Unmarshal(v, &ch) != nil {
				return true
			}
			if (key == "" || ch.Key == key) && (scope == "" || ch.Scope == scope) && (target == "" || ch.Target == target) {
				out = append(out, ch)
			}
			return len(out) < limit
		})
		return nil
	})
	return out
}

// ChangeAt is one change by its version.
func (s *Service) ChangeAt(version int64) (Change, bool) {
	var ch Change
	var ok bool
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		ch, ok = changeAt(tx, version)
		return nil
	})
	return ch, ok
}

// row reads one row inside a transaction, verifying its signature: one that fails is not there
// (and was already reported when it was loaded).
func (s *Service) row(tx kv.Tx, scope, target, key string) (Row, bool) {
	v := tx.Get(bucketSettings, rowKeyBytes(scope, target, key))
	if v == nil {
		return Row{}, false
	}
	var r Row
	if err := json.Unmarshal(v, &r); err != nil || !hmac.Equal(r.Sig, sign(s.key, r)) {
		return Row{}, false
	}
	return r, true
}

func putRow(tx kv.Tx, r Row) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if err := tx.Put(bucketSettings, rowKeyBytes(r.Scope, r.Target, r.Key), b); err != nil {
		return nil, err
	}
	return b, nil
}

func putChange(tx kv.Tx, key []byte, ch Change) error {
	if prev, ok := lastVerifiedChange(tx, key); ok {
		ch.PrevHash = prev.Hash
	}
	ch.Hash = hashChange(ch)
	ch.Sig = signChange(key, ch)
	b, err := json.Marshal(ch)
	if err != nil {
		return err
	}
	return tx.Put(bucketSettingsChanges, versionKeyBytes(ch.Version), b)
}

func changeAt(tx kv.Tx, version int64) (Change, bool) {
	v := tx.Get(bucketSettingsChanges, versionKeyBytes(version))
	if v == nil {
		return Change{}, false
	}
	var ch Change
	return ch, json.Unmarshal(v, &ch) == nil
}

// rowKey names a row's place: scope, target and key, apart.
func rowKey(scope, target, key string) string { return scope + "\x00" + target + "\x00" + key }

func rowKeyBytes(scope, target, key string) []byte { return []byte(rowKey(scope, target, key)) }

func labelFromRowKey(k string) string {
	parts := strings.Split(k, "\x00")
	if len(parts) != 3 {
		return k
	}
	return scopeName(parts[0], parts[1]) + " " + parts[2]
}

func scopeName(scope, target string) string {
	if target == "" {
		return scope
	}
	return scope + " " + target
}

// absent says a change's before or after side held nothing: never set, removed, or JSON null.
func absent(v json.RawMessage) bool { return len(v) == 0 || string(v) == "null" }

func versionKeyBytes(v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return b[:]
}

// nameRe is what a scope's target may be called: the design's names are lower-case, layered with
// slashes for tags. Collections, entitlements and requests are named by whoever makes them (W5,
// W7); they are checked there, against what exists — here only the shape is checked.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

func checkScopeTarget(scope, target string) error {
	if _, ok := levels[scope]; !ok {
		return fmt.Errorf("%w: scope is one of %s", ErrBadInput, strings.Join(Scopes, ", "))
	}
	if scope == ScopeSystem && target != "" {
		return fmt.Errorf("%w: the system scope has no target", ErrBadInput)
	}
	if scope != ScopeSystem {
		if target == "" {
			return fmt.Errorf("%w: the %s scope names what it applies to", ErrBadInput, scope)
		}
		if len(target) > 128 || !nameRe.MatchString(target) {
			return fmt.Errorf("%w: %q is not a name a setting may sit on", ErrBadInput, target)
		}
	}
	return nil
}

// sign is the row's signature: HMAC-SHA256 over the row as written, without the signature itself.
// The fields are separated by NUL, which none of them may hold, so two rows cannot sign the same.
func sign(key []byte, r Row) []byte {
	h := hmac.New(sha256.New, key)
	for _, part := range []string{r.Key, r.Scope, r.Target, strconvI64(r.Version), r.Actor, r.Via,
		strconvI64(r.At.UnixNano()), r.Reason, string(r.Value)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

func strconvI64(v int64) string { return strconv.FormatInt(v, 10) }

// macKey turns a 32-byte secret into the old HKDF construction. Tests use it to mint a row that
// only verifies under store.Secret("settings"), which Open and SetKey must ignore.
func macKey(secret []byte) []byte {
	prk := hkdf.Extract(sha256.New, secret, nil)
	out := make([]byte, 32)
	_, _ = io.ReadFull(hkdf.Expand(sha256.New, prk, []byte("beacon/policy-mac/v1")), out)
	return out
}

// SetKey moves the rows onto a new MAC key (the vault's SK). Rows that verify under the current
// key are re-signed; a row that only verifies under store.Secret("settings") stays invalid.
func (s *Service) SetKey(key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("%w: the signature key is 32 bytes", ErrBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnLeftovers()
	old := append([]byte(nil), s.key...)
	if len(old) == 0 {
		s.key = append([]byte(nil), key...)
		s.reload()
		return nil
	}
	if hmac.Equal(old, key) {
		s.reload()
		return nil
	}
	var committed signedhead.Head
	var wrote bool
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		res := s.table.Load(tx)
		if res.Status == signedhead.StatusFailsafe {
			s.key = append([]byte(nil), key...)
			return nil
		}
		puts := map[string][]byte{}
		for rk := range res.Head.Entries {
			v := tx.Get(bucketSettings, []byte(rk))
			if len(v) == 0 {
				continue
			}
			var r Row
			if json.Unmarshal(v, &r) != nil || !hmac.Equal(r.Sig, sign(old, r)) {
				continue
			}
			r.Sig = sign(key, r)
			b, err := json.Marshal(r)
			if err != nil {
				continue
			}
			if err := tx.Put(bucketSettings, []byte(rk), b); err != nil {
				return err
			}
			puts[rk] = b
		}
		tx.Range(bucketSettingsChanges, kv.Range{}, func(k, v []byte) bool {
			var ch Change
			if json.Unmarshal(v, &ch) != nil || ch.verifies(key) {
				return true
			}
			if !ch.verifies(old) {
				return true
			}
			ch.Sig = signChange(key, ch)
			b, err := json.Marshal(ch)
			if err != nil {
				return true
			}
			_ = tx.Put(bucketSettingsChanges, k, b)
			return true
		})
		s.key = append([]byte(nil), key...)
		h, err := s.table.CommitFrom(tx, res, puts, nil)
		if err != nil {
			s.key = old
			return headErr(err)
		}
		committed, wrote = h, true
		return nil
	})
	if err != nil {
		return err
	}
	if wrote {
		s.afterHead(committed)
	}
	s.reload()
	return nil
}

// RebuildHead is owner-only recovery: it scans the rows that still verify, writes a new signed
// head from them, and records the rebuild. Without it, a missing or bad head stays failsafe.
func (s *Service) RebuildHead(ctx context.Context, who Actor) error {
	if len(s.key) == 0 {
		return fmt.Errorf("%w: the settings have no signature key", ErrBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var committed signedhead.Head
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		rows := map[string][]byte{}
		tx.Range(bucketSettings, kv.Range{}, func(k, v []byte) bool {
			if string(k) == string(headKey) {
				return true
			}
			var r Row
			if json.Unmarshal(v, &r) != nil || !hmac.Equal(r.Sig, sign(s.key, r)) {
				return true
			}
			rows[string(k)] = append([]byte(nil), v...)
			return true
		})
		h, err := s.table.Rebuild(tx, rows)
		if err != nil {
			return err
		}
		committed = h
		return nil
	})
	if err != nil {
		return err
	}
	s.afterHead(committed)
	_ = s.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "settings_head_rebuild",
		Detail: "rebuilt the settings head from the rows that still verify"})
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(ctx, i18n.T(s.d.Lang, "%s rebuilt the settings head via %s", who.Name, s.via(who.Via)))
	}
	s.reload()
	return nil
}

func (s *Service) checkLeasePair(tx kv.Tx, key string, value json.RawMessage) error {
	if key != "storage.lease.renew" && key != "storage.lease.expires" {
		return nil
	}
	renew := s.durationAt(tx, "storage.lease.renew")
	expires := s.durationAt(tx, "storage.lease.expires")
	cur, err := ParseDuration(jsonString(value))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if key == "storage.lease.renew" {
		renew = cur
	} else {
		expires = cur
	}
	if renew >= expires {
		return fmt.Errorf("%w: storage.lease.expires must be longer than storage.lease.renew", ErrInvalid)
	}
	return nil
}

func (s *Service) durationAt(tx kv.Tx, key string) time.Duration {
	if r, ok := s.row(tx, ScopeSystem, "", key); ok {
		if d, err := ParseDuration(jsonString(r.Value)); err == nil {
			return d
		}
	}
	d, _ := ParseDuration(jsonString(Definitions[key].Default))
	return d
}

func requireReason(s *Service, reason string) error {
	want := 10
	if r, err := s.System("request.reason_min_length"); err == nil {
		if n, err := asInt(r.Value); err == nil {
			want = int(n)
		}
	}
	if want < 1 {
		want = 1
	}
	if len(strings.TrimSpace(reason)) < want {
		return fmt.Errorf("%w: a reason of at least %d characters", ErrBadInput, want)
	}
	return nil
}

func checkKeyScope(key, scope string) error {
	def, ok := Definitions[key]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknown, key)
	}
	if scope == ScopeRequest && !requestMaySet[key] {
		return fmt.Errorf("%w: %s cannot be set on a request", ErrBadInput, key)
	}
	if systemOnly[def.Group] && scope != ScopeSystem {
		return fmt.Errorf("%w: %s is set only at the system level", ErrBadInput, key)
	}
	return nil
}

var requestMaySet = map[string]bool{
	"request.default_uses":     true,
	"request.max_uses":         true,
	"request.default_duration": true,
	"request.max_duration":     true,
}

var systemOnly = map[string]bool{
	"retention": true, "backup": true, "storage": true, "import": true,
}
