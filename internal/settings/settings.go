// Package settings keeps Beacon's system configuration in its database: every knob the secrets
// work offers (request and grant limits, the review chain, rate limits, retention, import,
// backup), each with a default value, versions, history and revert, and a signature on every row
// so that someone holding the database file alone cannot widen a limit (the design's third
// invariant). A signed head names the live rows; a missing, stale or unverifiable row is served
// as the head's own value (fail closed), never as the looser default, and is reported to the
// alert chat.
//
// Values live at one of five scopes, and the one in force is resolved level by level: system,
// collection, tag, entitlement, request — the more specific wins, except that a request may only
// tighten what the level above allows, and several tags on one item take the stricter value.
// Approval levels do not travel this line; they take the strictest, as the design says elsewhere.
//
// The signature key is, for now, HKDF of the server-side secret the store keeps (store.Secret);
// when the root key of the vault lands, it becomes SK = HKDF(RK, "beacon/policy-mac/v1") — the
// same info string this package already uses, so the change is one re-signing pass over the rows
// (W5's migration; the rows carry everything needed to verify and re-sign them).
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
)

// Errors the API, MCP and the admin socket turn into answers.
var (
	ErrNotFound = errors.New("no such setting")
	ErrUnknown  = errors.New("unknown setting")
	ErrInvalid  = errors.New("the value does not fit this setting")
	ErrConflict = errors.New("the setting changed")
	ErrBadInput = errors.New("bad input")
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

// Deps are what the settings need. Bot may be nil.
type Deps struct {
	Store *store.Store
	Bot   Poster
	Lang  i18n.Lang
	Log   *slog.Logger
	Now   func() time.Time
}

// Service holds the settings: the rows in the database, verified, and the defaults registered in
// code (Definitions). Rows that fail their signature, or that the signed head names and the file
// no longer holds, are served as the head's own value (fail closed), reported to the log, the
// audit log and the alert chat, and listed by Bad for the console to show.
type Service struct {
	d   Deps
	mu  sync.Mutex // serialises changes
	key []byte     // the HMAC key, made once

	rowsMtx  sync.Mutex
	rows     map[string]Row     // by rowKey; only rows that verified and match the head
	broken   map[string]HeadEnt // head entries whose live row is missing or bad; served fail-closed
	bad      []string           // rows that did not verify, "scope/target/key"
	migrated bool               // the MAC key is the vault's; store.Secret("settings") is gone
}

// Open loads the settings from the store. A store that cannot be read fails. After the vault
// has taken over the MAC key, Open waits for SetKey; until then the defaults answer.
func Open(d Deps) (*Service, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Service{d: d}
	migrated, err := settingsKeyMigrated(d.Store)
	if err != nil {
		return nil, err
	}
	s.migrated = migrated
	if !migrated {
		secret, err := d.Store.Secret("settings")
		if err != nil {
			return nil, err
		}
		s.key = macKey(secret)
		s.reload()
	}
	return s, nil
}

func settingsKeyMigrated(st *store.Store) (bool, error) {
	var found bool
	err := st.Storage().View(func(tx kv.Tx) error {
		found = tx.Get(bucketMetaSettings, []byte("mac-migrated")) != nil
		return nil
	})
	return found, err
}

// bucketMetaSettings is a tiny marker bucket beside the rows, so a migrated MAC key is not
// confused with a settings row.
var bucketMetaSettings = []byte("settings_meta")

// reload reads the signed head and every row. Only rows the head names, at the version it
// names, and whose signature holds, are served. A named row that is missing or bad is kept
// as broken: Value serves the head's value (fail closed) and the break is reported.
func (s *Service) reload() {
	if len(s.key) == 0 {
		return
	}
	rows := map[string]Row{}
	broken := map[string]HeadEnt{}
	var bad []string
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		head, headOK := loadHead(tx, s.key)
		live := map[string]Row{}
		tx.Range(bucketSettings, kv.Range{}, func(k, v []byte) bool {
			if string(k) == string(headKey) {
				return true
			}
			var r Row
			if err := json.Unmarshal(v, &r); err != nil {
				bad = append(bad, "unreadable row")
				return true
			}
			if !hmac.Equal(r.Sig, sign(s.key, r)) {
				bad = append(bad, rowLabel(r))
				return true
			}
			live[rowKey(r.Scope, r.Target, r.Key)] = r
			return true
		})
		if !headOK {
			if anySignedChange(tx) {
				// a head that should be there is gone or forged: nothing from the file is trusted
				for k, r := range live {
					broken[k] = HeadEnt{Scope: r.Scope, Target: r.Target, Key: r.Key, Version: r.Version, Value: strictestOf(r.Key)}
					bad = append(bad, rowLabel(r)+" (head)")
				}
				tx.Range(bucketSettingsChanges, kv.Range{}, func(_, v []byte) bool {
					var ch Change
					if json.Unmarshal(v, &ch) != nil || absent(ch.After) {
						return true
					}
					k := rowKey(ch.Scope, ch.Target, ch.Key)
					if _, ok := broken[k]; ok {
						return true
					}
					broken[k] = HeadEnt{Scope: ch.Scope, Target: ch.Target, Key: ch.Key, Version: ch.Version, Value: strictestOf(ch.Key)}
					bad = append(bad, scopeName(ch.Scope, ch.Target)+" "+ch.Key+" (head)")
					return true
				})
				return nil
			}
			// first start, or the upgrade from unsigned heads: take the verified rows
			rows = live
			return nil
		}
		for _, e := range head.Rows {
			k := rowKey(e.Scope, e.Target, e.Key)
			r, ok := live[k]
			if !ok || r.Version != e.Version || string(r.Value) != string(e.Value) {
				broken[k] = e
				bad = append(bad, scopeName(e.Scope, e.Target)+" "+e.Key)
				continue
			}
			rows[k] = r
		}
		return nil
	})
	ub := unique(bad)
	s.rowsMtx.Lock()
	s.rows, s.broken, s.bad = rows, broken, ub
	s.rowsMtx.Unlock()
	for _, b := range ub {
		s.d.Log.Error("a settings row failed its signature and is served as the head's value", "row", b)
	}
	if len(bad) > 0 {
		_ = s.d.Store.Audit(store.AuditEntry{Actor: "beacon", Via: "system", Action: "settings_signature_invalid",
			Detail: fmt.Sprintf("%d row(s) failed their signature and are served fail-closed", len(unique(bad)))})
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
	_ = s.d.Bot.Send(context.Background(), i18n.T(s.d.Lang, "Settings rows failed their signature and are served as the strictest held value: %s.", strings.Join(rows, ", ")))
}

// Bad lists the rows that failed their signature, as "scope/target/key", for the console to show.
func (s *Service) Bad() []string {
	s.rowsMtx.Lock()
	defer s.rowsMtx.Unlock()
	return slices.Clone(s.bad)
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
	var out Change
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
		if err := putRow(tx, row); err != nil {
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
		out.Hash, out.Sig = "", nil // filled by putChange; reload from caller uses Version
		return writeHead(tx, s.key, out.Version)
	})
	if err != nil {
		return Change{}, err
	}
	s.reload()
	s.record(ctx, who, out)
	return out, nil
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
		return writeHead(tx, s.key, out.Version, rowKey(scope, target, key))
	})
	if err != nil {
		return Change{}, err
	}
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
		if absent(ch.Before) {
			// the change made the row: undoing it removes the row again
			if ok {
				if err := tx.Delete(bucketSettings, rowKeyBytes(ch.Scope, ch.Target, ch.Key)); err != nil {
					return err
				}
			}
		} else {
			back, err := def.canonical(ch.Before)
			if err != nil {
				return fmt.Errorf("%w: the value this change would restore no longer fits %s", ErrInvalid, ch.Key)
			}
			row.Value = back
			row.Sig = sign(s.key, row)
			if err := putRow(tx, row); err != nil {
				return err
			}
		}
		out = Change{Version: int64(next), Time: row.At, Actor: who.Name, Via: who.Via, Action: "revert",
			Reverts: version, Key: ch.Key, Scope: ch.Scope, Target: ch.Target, Before: before, After: row.Value, Reason: row.Reason}
		if err := putChange(tx, s.key, out); err != nil {
			return err
		}
		if absent(ch.Before) {
			return writeHead(tx, s.key, out.Version, rowKey(ch.Scope, ch.Target, ch.Key))
		}
		return writeHead(tx, s.key, out.Version)
	})
	if err != nil {
		return Change{}, err
	}
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

func putRow(tx kv.Tx, r Row) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return tx.Put(bucketSettings, rowKeyBytes(r.Scope, r.Target, r.Key), b)
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

func rowLabel(r Row) string { return scopeName(r.Scope, r.Target) + " " + r.Key }

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

// macKey turns the store's secret into the signature key: HKDF-SHA256 with the info string the
// design gives the policy signature key (SK = HKDF(RK, "beacon/policy-mac/v1")), so the same
// construction holds when the root key lands and the rows are re-signed onto it (W5's migration:
// read every row with the old key, write it with the new, one pass).
func macKey(secret []byte) []byte {
	prk := hkdf.Extract(sha256.New, secret, nil)
	out := make([]byte, 32)
	_, _ = io.ReadFull(hkdf.Expand(sha256.New, prk, []byte("beacon/policy-mac/v1")), out)
	return out
}

// SetKey moves the rows onto a new MAC key (the vault's SK) once. Rows that verify under the
// previous key are re-signed; store.Secret("settings") is deleted so a key that lived in the
// same database cannot mint a row that the next start would accept. Later calls only accept
// the current key: they never fall back to the deleted secret.
func (s *Service) SetKey(key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("%w: the signature key is 32 bytes", ErrBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := append([]byte(nil), s.key...)
	resigned := 0
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		prev, ok := loadHead(tx, old)
		if !ok {
			prev, _ = loadHead(tx, key)
		}
		var keys [][]byte
		tx.Range(bucketSettings, kv.Range{}, func(k, v []byte) bool {
			if string(k) == string(headKey) {
				return true
			}
			var r Row
			if json.Unmarshal(v, &r) != nil {
				return true
			}
			if hmac.Equal(r.Sig, sign(key, r)) {
				return true
			}
			if len(old) > 0 && hmac.Equal(r.Sig, sign(old, r)) {
				keys = append(keys, append([]byte(nil), k...))
			}
			return true
		})
		for _, k := range keys {
			v := tx.Get(bucketSettings, k)
			var r Row
			if json.Unmarshal(v, &r) != nil {
				continue
			}
			r.Sig = sign(key, r)
			if err := putRow(tx, r); err != nil {
				return err
			}
			resigned++
		}
		var changeKeys [][]byte
		tx.Range(bucketSettingsChanges, kv.Range{}, func(k, v []byte) bool {
			var ch Change
			if json.Unmarshal(v, &ch) != nil || ch.verifies(key) {
				return true
			}
			if len(old) > 0 && ch.verifies(old) {
				changeKeys = append(changeKeys, append([]byte(nil), k...))
			}
			return true
		})
		for _, k := range changeKeys {
			v := tx.Get(bucketSettingsChanges, k)
			var ch Change
			if json.Unmarshal(v, &ch) != nil {
				continue
			}
			ch.Sig = signChange(key, ch)
			b, err := json.Marshal(ch)
			if err != nil {
				return err
			}
			if err := tx.Put(bucketSettingsChanges, k, b); err != nil {
				return err
			}
		}
		var latest int64
		if ch, ok := lastChange(tx); ok {
			latest = ch.Version
		}
		if err := writeHeadFrom(tx, key, latest, prev); err != nil {
			return err
		}
		return tx.Put(bucketMetaSettings, []byte("mac-migrated"), []byte("1"))
	})
	if err != nil {
		return err
	}
	s.key = append([]byte(nil), key...)
	if !s.migrated {
		if err := s.d.Store.DeleteSecret("settings"); err != nil {
			return err
		}
	}
	s.migrated = true
	s.reload()
	if resigned > 0 {
		s.d.Log.Info("the settings' rows were re-signed onto the vault's signature key", "rows", resigned)
	}
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

func strictestOf(key string) json.RawMessage {
	d, ok := Definitions[key]
	if !ok {
		return nil
	}
	if d.kind.stricter == nil {
		return d.Default
	}
	// the stricter of the default and a tight stand-in, when the kind orders it
	tight := tightValue(d)
	if tight == nil {
		return d.Default
	}
	w, ok := d.stricter(d.Default, tight)
	if !ok {
		return tight
	}
	return w
}

func tightValue(d *Definition) json.RawMessage {
	switch d.kind.name {
	case "count":
		return mustJSON(1)
	case "int":
		return mustJSON(1)
	case "duration":
		if d.Group == "retention" {
			return mustJSON("24h")
		}
		return mustJSON("1s")
	case "enum":
		return d.Default // enum stricter is first in order; default may already be it
	case "bool":
		return mustJSON(false)
	}
	return d.Default
}
