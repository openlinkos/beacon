// Package secrets stores encrypted, destination-bound team credentials and lets agents use them
// through explicit, expiring grants. It gives a caller the outcome, without giving it the value.
package secrets

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

const (
	UseScope         = "secrets:use"
	ManageScope      = "accounts:write"
	maxGrantLifetime = 90 * 24 * time.Hour
	checkContext     = "beacon/team-credential/key-check/v1"
)

var (
	ErrAccess        = errors.New("the current identity has no access to this team credential")
	ErrInput         = errors.New("the credential or grant details are invalid")
	ErrConflict      = errors.New("the credential changed; reload it and try again")
	ErrState         = errors.New("the team credential store cannot be verified")
	ErrAnchorPending = errors.New("the credential change was written to the database; its rollback anchor is awaiting verification")
	ErrUpstream      = errors.New("the configured service could not be reached")

	bucket   = []byte("team_credentials")
	checkKey = []byte("key-check")
	headKey  = []byte("signed-head")
	idRE     = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

type Bot interface {
	Send(context.Context, string) error
}

type Deps struct {
	Store        *store.Store
	AnchorDir    string
	AllowedHosts []string
	Bot          Bot
	Lang         i18n.Lang
	Log          *slog.Logger
	Now          func() time.Time
	RootCAs      *x509.CertPool // nil uses the system trust store
}

type Service struct {
	d       Deps
	aead    cipher.AEAD
	head    signedhead.Table
	allowed map[string]bool
	http    *http.Client

	mu            sync.Mutex // serialises the signed head and its filesystem anchor
	pendingAnchor signedhead.Head
}

// Grant identifies one specific Agent account and its limit. Created prevents a deleted agent from
// recovering an old delegation when someone registers its name again.
type Grant struct {
	Agent   string    `json:"agent"`
	Owner   string    `json:"owner"`
	Created time.Time `json:"agent_created"`
	Until   time.Time `json:"until"`
}

// Credential is the metadata a caller may see. The value is never part of it.
type Credential struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Endpoint string    `json:"endpoint,omitempty"`
	Version  int64     `json:"version"`
	Updated  time.Time `json:"updated"`
	Grants   []Grant   `json:"grants,omitempty"`
}

type row struct {
	Credential
	Ciphertext []byte `json:"ciphertext"`
}

type NewCredential struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Value    string `json:"value"`
}

func Open(key []byte, d Deps) (*Service, error) {
	if d.Store == nil || d.AnchorDir == "" || len(d.AllowedHosts) == 0 {
		return nil, ErrInput
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	a, mac, err := derives(key)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, host := range d.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		u, err := url.Parse("https://" + host)
		if err != nil || u.Host != host || u.Hostname() == "" || u.User != nil || strings.ContainsAny(host, "/?#*%") {
			clear(mac)
			return nil, ErrInput
		}
		allowed[host] = true
	}
	s := &Service{d: d, aead: a, allowed: allowed, http: credentialClient(d.RootCAs)}
	s.head = signedhead.Table{
		Name: "team-credentials", Bucket: bucket, HeadKey: headKey, SK: func() []byte { return mac },
		Anchor: signedhead.FileAnchor{Dir: d.AnchorDir},
		Alert:  func(msg string) { d.Log.Error("team credential policy cannot be verified", "reason", msg) },
	}
	if err := s.checkKey(); err != nil {
		clear(mac)
		return nil, err
	}
	return s, nil
}

func (s *Service) checkKey() error {
	return s.d.Store.Storage().Update(func(tx kv.Tx) error {
		tracked, err := s.head.Anchor.Tracks(s.head.Name)
		if err != nil || !tracked && tx.Get(bucket, headKey) != nil {
			return ErrState // a populated store needs an independently restored anchor
		}
		if !tracked {
			if err := s.head.Anchor.Record(s.head.Name, 0); err != nil {
				return ErrState
			}
		}
		h := s.loadHead(tx)
		if h.Status == signedhead.StatusFailsafe {
			return ErrState
		}
		box := tx.Get(bucket, checkKey)
		if len(box) != 0 {
			p, err := unseal(s.aead, box, checkContext)
			clear(p)
			return err
		}
		if h.Status != signedhead.StatusEmpty {
			return ErrState
		}
		box, err = seal(s.aead, []byte(checkContext), checkContext)
		if err != nil {
			return err
		}
		return tx.Put(bucket, checkKey, box)
	})
}

func (s *Service) loadHead(tx kv.Tx) signedhead.LoadResult {
	if tracked, err := s.head.Anchor.Tracks(s.head.Name); err != nil || !tracked {
		s.d.Log.Error("the team credential rollback anchor is absent or unreadable")
		return signedhead.LoadResult{Status: signedhead.StatusFailsafe}
	}
	return s.head.Load(tx)
}

// CanManage admits people signed in with owner accounts.
func CanManage(p store.Principal) bool { return p.Person() && p.Has(ManageScope) }

func (s *Service) target(endpoint string) (*url.URL, bool) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(endpoint, "#") || len(endpoint) > 2000 {
		return nil, false
	}
	return u, s.allowed[strings.ToLower(u.Host)]
}

func validValue(value string) bool {
	return len(value) >= 8 && len(value) <= 4096 && !strings.ContainsFunc(value, func(r rune) bool {
		return r < '!' || r > '~'
	})
}

func (s *Service) Create(ctx context.Context, p store.Principal, via string, in NewCredential) (Credential, error) {
	in.Name, in.Endpoint = strings.TrimSpace(in.Name), strings.TrimSpace(in.Endpoint)
	if !validValue(in.Value) || in.Name == "" || !utf8.ValidString(in.Name) ||
		utf8.RuneCountInString(in.Name) > 100 || strings.ContainsFunc(in.Name, unicode.IsControl) ||
		strings.Contains(in.Name, in.Value) || strings.Contains(in.Endpoint, in.Value) {
		return Credential{}, ErrInput
	}
	if _, ok := s.target(in.Endpoint); !ok {
		return Credential{}, ErrInput
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Credential{}, err
	}
	id := hex.EncodeToString(random)
	return s.change(ctx, p, via, id, 0, "credential.create", func(_ kv.Tx, r *row) error {
		r.Name, r.Endpoint = in.Name, in.Endpoint
		return s.replaceValue(r, in.Value)
	})
}

func (s *Service) Rotate(ctx context.Context, p store.Principal, via, id string, version int64, value string) (Credential, error) {
	if version < 1 || !validValue(value) {
		return Credential{}, ErrInput
	}
	return s.change(ctx, p, via, id, version, "credential.rotate", func(_ kv.Tx, r *row) error {
		if strings.Contains(r.Name, value) || strings.Contains(r.Endpoint, value) {
			return ErrInput
		}
		return s.replaceValue(r, value)
	})
}

func (s *Service) replaceValue(r *row, value string) error {
	bytes := []byte(value)
	defer clear(bytes)
	var err error
	r.Ciphertext, err = seal(s.aead, bytes, rowContext(r.Credential))
	return err
}

func rowContext(c Credential) string {
	return "beacon/team-credential/value/v1\x00" + c.ID + "\x00" + c.Endpoint
}

// SetGrant adds or narrows the grant to the named agent. Its lifetime is bounded, and any later
// demotion, deletion or credential revocation is also checked when an agent tries to use it.
func (s *Service) SetGrant(ctx context.Context, p store.Principal, via, id string, version int64, agent string, until time.Time) (Credential, error) {
	if version < 1 || store.ValidAgentName(agent) != nil {
		return Credential{}, ErrInput
	}
	return s.change(ctx, p, via, id, version, "credential.grant", func(tx kv.Tx, r *row) error {
		now := s.d.Now()
		recipient, a, ok := s.d.Store.TeamAgent(tx, agent)
		if !ok || !recipient.Has(UseScope) || !until.After(now) || until.After(now.Add(maxGrantLifetime)) {
			return ErrInput
		}
		g := Grant{Agent: a.Name, Owner: a.Owner, Created: a.Created, Until: until.UTC()}
		r.Grants = slices.DeleteFunc(r.Grants, func(held Grant) bool { return held.Agent == g.Agent })
		r.Grants = append(r.Grants, g)
		return nil
	}, agent+" (until "+until.UTC().Format(time.RFC3339)+")")
}

func (s *Service) Revoke(ctx context.Context, p store.Principal, via, id string, version int64, agent string) (Credential, error) {
	if version < 1 || store.ValidAgentName(agent) != nil {
		return Credential{}, ErrInput
	}
	return s.change(ctx, p, via, id, version, "credential.revoke", func(_ kv.Tx, r *row) error {
		old := len(r.Grants)
		r.Grants = slices.DeleteFunc(r.Grants, func(g Grant) bool { return g.Agent == agent })
		if len(r.Grants) == old {
			return ErrAccess
		}
		return nil
	}, agent)
}

func (s *Service) change(ctx context.Context, p store.Principal, via, id string, version int64, action string,
	update func(kv.Tx, *row) error, targets ...string) (Credential, error) {
	if !idRE.MatchString(id) {
		return Credential{}, ErrInput
	}
	if !CanManage(p) {
		return Credential{}, ErrAccess
	}
	detail := strings.Join(targets, " ")
	result, err := s.commit(ctx, p, via, id, version, action, detail, update)
	if err != nil {
		return Credential{}, err
	}
	s.tell(ctx, p, via, action, detail, id)
	return result, nil
}

func (s *Service) commit(ctx context.Context, p store.Principal, via, id string, version int64, action, detail string,
	update func(kv.Tx, *row) error) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repairAnchor(); err != nil {
		return Credential{}, err
	}
	var out Credential
	var head signedhead.Head
	err := s.d.Store.Storage().UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		current, _, ok := s.d.Store.TeamPrincipal(tx, p)
		if !ok || !CanManage(current) {
			return ErrAccess
		}
		h := s.loadHead(tx)
		if h.Status == signedhead.StatusFailsafe {
			return ErrState
		}
		r := row{Credential: Credential{ID: id}}
		b := tx.Get(bucket, []byte(id))
		switch {
		case version > 0 && !signedhead.Accept(h, id, b):
			return ErrAccess
		case version > 0:
			if json.Unmarshal(b, &r) != nil || r.ID != id {
				return ErrState
			}
			if r.Version != version {
				return ErrConflict
			}
		case len(b) != 0:
			return ErrConflict
		}
		if err := update(tx, &r); err != nil {
			return err
		}
		r.Version++
		r.Updated = s.d.Now().UTC()
		encoded, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := tx.Put(bucket, []byte(id), encoded); err != nil {
			return err
		}
		head, err = s.head.CommitFrom(tx, h, map[string][]byte{id: encoded}, nil)
		if err != nil {
			return ErrState
		}
		if err := s.d.Store.AppendAuditTx(raw, events.Event{Actor: current.Name(), Via: via, Action: action, Object: id, Detail: detail}); err != nil {
			return err
		}
		out = r.Credential
		return nil
	})
	if err != nil {
		return Credential{}, err
	}
	if err := s.head.AfterCommit(head); err != nil {
		s.d.Log.ErrorContext(ctx, "could not anchor the team credentials change", "err", err)
		s.pendingAnchor = head
		return Credential{}, ErrAnchorPending
	}
	return out, nil
}

// repairAnchor requires an in-process retry even when a failed directory sync left a readable
// epochs file. It runs under mu before a read, use or later mutation.
func (s *Service) repairAnchor() error {
	if s.pendingAnchor.Epoch == 0 {
		return nil
	}
	if err := s.head.AfterCommit(s.pendingAnchor); err != nil {
		return ErrAnchorPending
	}
	s.pendingAnchor = signedhead.Head{}
	return nil
}

func (s *Service) tell(ctx context.Context, p store.Principal, via, action, detail, id string) {
	if s.d.Bot != nil {
		text := i18n.T(s.d.Lang, "%s performed %s on team credential %s via %s", p.Name(), action, id, via)
		if detail != "" {
			text += " (" + detail + ")"
		}
		if err := s.d.Bot.Send(ctx, text); err != nil {
			s.d.Log.WarnContext(ctx, "could not echo the team credential action")
		}
	}
}

func (s *Service) accessible(r row, a store.Agent) (Grant, bool) {
	for _, g := range r.Grants {
		if g.Agent == a.Name && g.Owner == a.Owner && g.Created.Equal(a.Created) && s.d.Now().Before(g.Until) {
			return g, true
		}
	}
	return Grant{}, false
}

// List returns all items to an owner, and only live, explicitly delegated items to an agent.
func (s *Service) List(p store.Principal) ([]Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repairAnchor(); err != nil {
		return nil, err
	}
	out := []Credential{}
	err := s.d.Store.Storage().View(func(tx kv.Tx) error {
		current, a, ok := s.d.Store.TeamPrincipal(tx, p)
		manage := CanManage(p) && CanManage(current)
		if !ok || (!manage && (current.Agent == "" || !current.Has(UseScope) || !p.Has(UseScope))) {
			return ErrAccess
		}
		h := s.loadHead(tx)
		if h.Status == signedhead.StatusFailsafe {
			return ErrState
		}
		for id := range h.Head.Entries {
			b := tx.Get(bucket, []byte(id))
			var r row
			if !signedhead.Accept(h, id, b) || json.Unmarshal(b, &r) != nil || r.ID != id {
				s.d.Log.Warn("ignored an unverified team credential")
				continue
			}
			if manage {
				out = append(out, r.Credential)
			} else if grant, ok := s.accessible(r, a); ok {
				out = append(out, Credential{ID: r.ID, Name: r.Name, Version: r.Version, Updated: r.Updated, Grants: []Grant{grant}})
			}
		}
		return nil
	})
	slices.SortFunc(out, func(a, b Credential) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, err
}

func (s *Service) AgentChoices() []string {
	names := []string{}
	for _, a := range s.d.Store.Agents("") {
		p, ok := s.d.Store.AccountPrincipal(a.Owner)
		if ok && p.Has(UseScope) && slices.Contains(a.Wanted(), UseScope) {
			names = append(names, a.Name)
		}
	}
	slices.Sort(names)
	return names
}
