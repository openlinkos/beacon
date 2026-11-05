package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/store"
)

// The buckets, beside the store's own. The names carry the "secret_" prefix so they cannot meet
// another package's.
var (
	bucketCollections  = []byte("secret_collections")
	bucketKeys         = []byte("secret_keys")         // the wrapped collection keys, every version
	bucketItems        = []byte("secret_items")        // the overviews, signed
	bucketVersions     = []byte("secret_versions")     // a version's fields and ciphertext
	bucketFiles        = []byte("secret_attachments")  // the attachments' ciphertexts
	bucketTags         = []byte("secret_tags")         // the tags, signed
	bucketPerms        = []byte("secret_perms")        // the people permissions, signed
	bucketMeta         = []byte("secret_meta")         // the key check and the like
	bucketEntitlements = []byte("secret_entitlements") // the agents' entitlements, signed
	bucketAgentTags    = []byte("secret_agent_tags")   // the agents' grouping tags, signed
	bucketRequests     = []byte("secret_requests")     // the requests and their decisions
	bucketGrants       = []byte("secret_grants")       // the grants, signed
)

// The keys of the meta bucket.
const metaKeyCheck = "keycheck"

// keyRow is one version of a collection key, wrapped under the root key.
type keyRow struct {
	Collection string    `json:"collection"`
	Version    int64     `json:"version"`
	Wrapped    []byte    `json:"wrapped"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

// Errors the API, MCP and the CLI turn into answers.
var (
	ErrNotFound   = errors.New("no such collection, item, tag or field")
	ErrExists     = errors.New("a collection or tag of this name exists")
	ErrInvalid    = errors.New("the value or shape does not fit")
	ErrConflict   = errors.New("it changed since you saw it")
	ErrBadInput   = errors.New("bad input")
	ErrForbidden  = errors.New("you may not do this here")
	ErrReauth     = errors.New("enter your password to view values")
	ErrAgent      = errors.New("agents reach items through grants, not this door: request_secret asks, get_secret takes")
	ErrSealedUp   = ErrSealed
	ErrAttachment = errors.New("the attachment is too large")
)

// Poster sends a plain message to the alert chat.
type Poster interface {
	Send(ctx context.Context, text string) error
}

// Deps are what the vault needs. Bot and Settings may be nil (defaults then).
type Deps struct {
	Store    *store.Store
	Bot      Poster
	Settings *settings.Service
	Lang     i18n.Lang
	Log      *slog.Logger
	Now      func() time.Time
	// Clock of the HTTP client the KMS modes use; nil: the default.
	HTTP *http.Client
	// CheckPassword verifies an account's password for the reveal re-authentication; nil: the
	// store's own sign-in.
	CheckPassword func(username, password string) error
	// Audit writes an audit entry; nil: the store's own. A test can refuse one, which is how the
	// record-before-give rule (no record, no value) is checked for people's views.
	Audit func(e store.AuditEntry) error
	// AppendUse writes a use record inside the transaction a get commits by (the agents' half of
	// the record-before-give rule); nil: the store's uses log. A test can refuse one.
	AppendUse func(tx *sql.Tx, e *events.Event) error
	// Hooks pushes the secret events to an agent's hook (secret.granted, secret.denied,
	// secret.expiring, secret.changed, secret.revoked), signed and delivered the way the on-call
	// hooks are; nil: no agent is woken.
	Hooks HookSender
	// Cards posts the Feishu card a person decides a request on; nil: the chat only hears of it.
	Cards CardSender
	// AlertState tells whether the alert group a grant is bound to is firing, and when it
	// resolved; nil: no grant is bound to an alert's grace.
	AlertState func(group string) (firing bool, resolvedAt time.Time, ok bool)
	// ReviewAgent tells whether the named agent may decide requests as a review agent
	// (the secrets:review scope); nil: nobody does.
	ReviewAgent func(agent string) bool
	// Deliver writes a declared file to a machine, as the owner and with the mode given (the SSH
	// channel's writer); nil: nothing is delivered, the state and the log say so.
	Deliver FileDeliverer
	// Maintenance says whether a maintenance window is on, for file.on_change's next_window; nil:
	// no window ever is, and the delivery waits for one until the setting changes.
	Maintenance func() bool
}

// Service is the vault: the collections with their keys, the items with their versions, the tags
// and the people permissions, all in the store's database, the policy rows signed under SK.
type Service struct {
	d    Deps
	cfg  SealConfig
	ctx  context.Context
	keys rootKeys

	keyChange func(sk []byte) // called when SK changes (unseal, rotate), with the new key
	kcMu      sync.Mutex

	mu sync.Mutex // serialises writes

	reauthMu sync.Mutex
	reauth   map[string]time.Time // by user name: until when a reveal needs no password

	rotating map[string]bool // collections whose key is being rotated
	rotMu    sync.Mutex

	badMu sync.Mutex
	bad   []string // rows that failed their signature, for the console and the log
}

// GrantsDeps is the grants' own dependencies, set after the vault opens (SetGrantsDeps): the
// events' delivery, the approval cards, the alert state a bound grant outlives, and the
// review-agent check. A nil field leaves what was set before.
type GrantsDeps struct {
	Hooks       HookSender
	Cards       CardSender
	AlertState  func(group string) (firing bool, resolvedAt time.Time, ok bool)
	ReviewAgent func(agent string) bool
}

// SetGrantsDeps fills the grants' dependencies; only the fields that are set move.
func (s *Service) SetGrantsDeps(d GrantsDeps) {
	if d.Hooks != nil {
		s.d.Hooks = d.Hooks
	}
	if d.Cards != nil {
		s.d.Cards = d.Cards
	}
	if d.AlertState != nil {
		s.d.AlertState = d.AlertState
	}
	if d.ReviewAgent != nil {
		s.d.ReviewAgent = d.ReviewAgent
	}
}

// OnKeyChange registers what runs when the signature key changes — the settings re-sign onto the
// new key, which is the wiring's one line. It runs after the vault has settled on the new key.
func (s *Service) OnKeyChange(f func(sk []byte)) {
	s.kcMu.Lock()
	s.keyChange = f
	s.kcMu.Unlock()
}

// keyChanged runs the registered callback, if any. A callback that fails is logged: the settings
// then still verify under the old key, which the next successful change fixes.
func (s *Service) keyChanged() {
	s.kcMu.Lock()
	f := s.keyChange
	s.kcMu.Unlock()
	if f == nil {
		return
	}
	sk := s.SK()
	f(sk)
}

// Open brings the vault up: it makes what the seal mode needs at the first start and unseals what
// unseals itself. Rows that fail their signature are served as absent and reported, both to the log
// and the audit log (the third invariant); they are listed by Bad.
func Open(ctx context.Context, cfg SealConfig, d Deps) (*Service, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.Store == nil {
		return nil, errors.New("the vault needs the store")
	}
	if cfg.Mode == "" {
		cfg.Mode = SealFile
	}
	if !ValidSealMode(cfg.Mode) {
		return nil, fmt.Errorf("unknown seal mode %q; one of %s", cfg.Mode, joinModes())
	}
	if cfg.Dir == "" {
		return nil, errors.New("the vault needs its directory")
	}
	s := &Service{d: d, cfg: cfg, reauth: map[string]time.Time{}, rotating: map[string]bool{}}
	if d.CheckPassword == nil {
		d.CheckPassword = func(username, password string) error {
			if _, err := d.Store.SignIn(username, password); err != nil {
				return fmt.Errorf("the password does not check")
			}
			return nil
		}
	}
	s.d.CheckPassword = d.CheckPassword
	if d.Audit != nil {
		s.d.Audit = d.Audit
	} else {
		s.d.Audit = s.d.Store.Audit
	}
	s.ctx = ctx
	if err := s.initSeal(); err != nil {
		return nil, err
	}
	s.reloadSignedRows()
	return s, nil
}

func (s *Service) now() time.Time { return s.d.Now().UTC() }

// auditEntry writes an audit entry through the hook (the store's own unless a test stands in).
func (s *Service) auditEntry(e store.AuditEntry) error { return s.d.Audit(e) }

// ---- meta, keys and signatures

func metaPutTx(tx kv.Tx, key string, v []byte) error { return tx.Put(bucketMeta, []byte(key), v) }

func (s *Service) metaPut(key string, v []byte) error {
	return s.d.Store.Storage().Update(func(tx kv.Tx) error { return metaPutTx(tx, key, v) })
}

func (s *Service) metaGet(key string) ([]byte, bool) {
	var out []byte
	ok := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		out, ok = tx.Get(bucketMeta, []byte(key)), tx.Get(bucketMeta, []byte(key)) != nil
		return nil
	})
	return out, ok
}

func putKeyRow(tx kv.Tx, kr keyRow) error {
	b, err := json.Marshal(kr)
	if err != nil {
		return err
	}
	return tx.Put(bucketKeys, keyRowKey(kr.Collection, kr.Version), b)
}

func keyRowKey(collection string, version int64) []byte {
	return []byte(fmt.Sprintf("%s\x00%016x", collection, version))
}

// collectionKey unwraps the collection key of one version under the root key.
func (s *Service) collectionKey(collection string, version int64) ([]byte, error) {
	rk, _, err := s.keys.keys()
	if err != nil {
		return nil, err
	}
	var kr keyRow
	found := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		v := tx.Get(bucketKeys, keyRowKey(collection, version))
		found = v != nil && json.Unmarshal(v, &kr) == nil
		return nil
	})
	if !found {
		return nil, fmt.Errorf("%w: collection key version %d", ErrNotFound, version)
	}
	ck, err := unwrap(rk, kr.Wrapped, keyWrapAAD(collection, version))
	if err != nil {
		return nil, err
	}
	return ck, nil
}

// newCollectionKey makes the first version of a collection's key, wrapped under the root key.
func (s *Service) newCollectionKey(collection string) error {
	rk, _, err := s.keys.keys()
	if err != nil {
		return err
	}
	ck, err := randomKey()
	if err != nil {
		return err
	}
	defer clear(ck)
	w, err := wrap(rk, ck, keyWrapAAD(collection, 1))
	if err != nil {
		return err
	}
	return s.d.Store.Storage().Update(func(tx kv.Tx) error {
		return putKeyRow(tx, keyRow{Collection: collection, Version: 1, Wrapped: w, At: s.now(), Actor: "beacon"})
	})
}

// sk returns the signature key, failing when sealed.
func (s *Service) sk() ([]byte, error) {
	_, sk, err := s.keys.keys()
	return sk, err
}

// ---- signed rows

// signedCollection is a collection row as stored, with its signature.
type signedCollection struct {
	Collection
	Sig []byte `json:"sig"`
}

func (c Collection) signedParts() []string {
	return []string{c.ID, c.Name, c.Description, c.Level, strings.Join(c.Actions, "\x01"), i64(c.KeyVersion), c.CreatedBy, i64(c.Created.UnixNano())}
}

// signedItem is an item overview row as stored, with its signature.
type signedItem struct {
	Item
	Sig []byte `json:"sig"`
}

func (i Item) signedParts() []string {
	return []string{i.ID, i.Collection, i.Title, i.Category, strings.Join(i.URLs, "\x01"), strings.Join(i.Tags, "\x01"),
		i.Level, strings.Join(i.Actions, "\x01"), i.State, i64(i.Version), i.Source, i64(int64(i.RotateDays)),
		strings.Join(i.Machines, "\x01"), strings.Join(i.Hosts, "\x01"), filesKey(i.Files), i.CreatedBy, i64(i.Created.UnixNano())}
}

// filesKey is the files of an item as one signed string.
func filesKey(files []ItemFile) string {
	parts := make([]string, 0, len(files))
	for _, f := range files {
		parts = append(parts, strings.Join([]string{f.Machine, f.Path, f.Field, f.Owner, f.Mode}, "\x02"))
	}
	return strings.Join(parts, "\x01")
}

// signedTag is a tag row as stored, with its signature.
type signedTag struct {
	Tag
	Sig []byte `json:"sig"`
}

func (t Tag) signedParts() []string {
	return []string{t.Name, t.Level, strings.Join(t.Actions, "\x01"), t.Description, i64(t.Created.UnixNano())}
}

func (p Permission) signedParts() []string {
	return []string{p.Collection, p.Subject, p.Perm, p.Actor, p.Via, i64(p.At.UnixNano()), p.Reason}
}

// reloadSignedRows reads every policy row, verifies each signature and keeps what verified in the
// in-memory tables. This is the only place the rows come in, so nothing unverified is ever served.
// Unreadable or failing rows are reported: to the log, to the audit log (an alarm), and to Bad.
func (s *Service) reloadSignedRows() {
	// the tables are read through the database on each operation; what this keeps is the list of
	// rows that fail, for the console and the reporting
	var bad []string
	report := func(kind, label string) {
		bad = append(bad, kind+" "+label)
		s.d.Log.Error("a policy row failed its signature and is treated as absent", "kind", kind, "row", label)
	}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketCollections, kv.Range{}, func(_, v []byte) bool {
			var r signedCollection
			if json.Unmarshal(v, &r) != nil {
				report("collection", "unreadable")
				return true
			}
			if sk, err := s.sk(); err == nil && !verifyRow(sk, r.Sig, r.signedParts()...) {
				report("collection", r.Name)
			}
			return true
		})
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil {
				report("item", "unreadable")
				return true
			}
			if sk, err := s.sk(); err == nil && !verifyRow(sk, r.Sig, r.signedParts()...) {
				report("item", r.Title)
			}
			return true
		})
		tx.Range(bucketTags, kv.Range{}, func(_, v []byte) bool {
			var r signedTag
			if json.Unmarshal(v, &r) != nil {
				report("tag", "unreadable")
				return true
			}
			if sk, err := s.sk(); err == nil && !verifyRow(sk, r.Sig, r.signedParts()...) {
				report("tag", r.Name)
			}
			return true
		})
		tx.Range(bucketPerms, kv.Range{}, func(_, v []byte) bool {
			var r Permission
			if json.Unmarshal(v, &r) != nil {
				report("permission", "unreadable")
				return true
			}
			if sk, err := s.sk(); err == nil && !verifyRow(sk, r.Sig, r.signedParts()...) {
				report("permission", r.Subject+" on "+r.Collection)
			}
			return true
		})
		tx.Range(bucketEntitlements, kv.Range{}, func(_, v []byte) bool {
			var r Entitlement
			if json.Unmarshal(v, &r) != nil {
				report("entitlement", "unreadable")
				return true
			}
			if sk, err := s.sk(); err == nil && !verifyRow(sk, r.Sig, r.signedParts()...) {
				report("entitlement", r.Name)
			}
			return true
		})
		tx.Range(bucketGrants, kv.Range{}, func(_, v []byte) bool {
			var r Grant
			if json.Unmarshal(v, &r) != nil {
				report("grant", "unreadable")
				return true
			}
			if sk, err := s.sk(); err == nil && !verifyRow(sk, r.Sig, r.signedParts()...) {
				report("grant", r.ID)
			}
			return true
		})
		tx.Range(bucketAgentTags, kv.Range{}, func(_, v []byte) bool {
			var r AgentTags
			if json.Unmarshal(v, &r) != nil {
				report("agent tags", "unreadable")
				return true
			}
			if sk, err := s.sk(); err == nil && !verifyRow(sk, r.Sig, r.signedParts()...) {
				report("agent tags", r.Agent)
			}
			return true
		})
		return nil
	})
	s.badMu.Lock()
	s.bad = bad
	s.badMu.Unlock()
	if len(bad) > 0 {
		_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_signature_invalid",
			Detail: fmt.Sprintf("%d vault row(s) failed their signature and are treated as absent", len(bad))})
		_ = s.alarm("vault rows failed their signature", fmt.Sprintf("%d row(s) are treated as absent; see the console", len(bad)))
		s.anomalyFired("signature_failure", fmt.Sprintf("%d row(s) failed their signature", len(bad)))
	}
}

// Bad lists the rows that failed their signature, as "kind label", for the console to show.
func (s *Service) Bad() []string {
	s.badMu.Lock()
	defer s.badMu.Unlock()
	return append([]string(nil), s.bad...)
}

// CheckRows verifies every policy row now and returns the failing labels; the console and the CLI
// offer it after a restore or a suspicion.
func (s *Service) CheckRows() []string {
	s.reloadSignedRows()
	return s.Bad()
}

// alarm tells the alert chat, when there is one.
func (s *Service) alarm(what, detail string) error {
	if s.d.Bot == nil {
		return nil
	}
	return s.d.Bot.Send(s.ctx, s.t("%s: %s", what, detail))
}

func (s *Service) t(format string, args ...any) string {
	return i18n.T(s.d.Lang, format, args...)
}

// auditKey records a key event (rotate, seal, unseal).
func (s *Service) auditKey(action, detail string) {
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_key_" + action, Detail: detail})
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("The vault's root key was %s.", actionWord(action)))
	}
}

func actionWord(action string) string {
	switch action {
	case "rotate":
		return "rotated"
	case "seal":
		return "sealed"
	default:
		return "unsealed"
	}
}

// newID makes a random 16-hex-digit ID for a collection or an item.
func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
