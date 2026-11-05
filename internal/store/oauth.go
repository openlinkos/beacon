package store

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// What MCP's OAuth leaves in the store (package oauth speaks the protocol): the clients that registered
// themselves, and the grants a person allowed, each an agent's credential. An access token lasts an hour
// and is opaque, kept as its SHA-256 like a token; a refresh token is replaced by every use, and the use
// of one already replaced revokes its grant, since only a thief would send it.

var (
	bucketClients = []byte("oauth_clients")
	bucketGrants  = []byte("oauth_grants")
	bucketAccess  = []byte("oauth_access")
)

const (
	// AccessPrefix starts an OAuth access token, RefreshPrefix a refresh token.
	AccessPrefix  = "lba_"
	RefreshPrefix = "lbr_"
	// AccessTTL is how long an access token lasts.
	AccessTTL = time.Hour
	// ClientUnused is how long a client registration is kept if it is never used to sign in.
	ClientUnused = 24 * time.Hour
	// MaxClients bounds the registrations kept.
	MaxClients = 1000
	// keepSpent is how many replaced refresh tokens a grant remembers.
	keepSpent = 20
)

// OAuth errors.
var (
	// ErrBadGrant means a refresh token is unknown, expired or not the client's.
	ErrBadGrant = errors.New("the grant is unknown, expired or revoked")
	// ErrReplayed means a refresh token was used again after it was replaced; the grant is revoked.
	ErrReplayed = errors.New("the refresh token was used already; the grant is revoked")
	// ErrTooMany means there are too many client registrations.
	ErrTooMany = errors.New("too many client registrations; try again later")
)

// OAuthClient is a client that registered itself (RFC 7591): a public client, with the redirect URIs it
// may use. Used is when it was first used to sign in; one never used is forgotten after ClientUnused.
type OAuthClient struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirect_uris"`
	URI          string    `json:"uri,omitempty"`
	Created      time.Time `json:"created"`
	Used         time.Time `json:"used,omitzero"`
}

// Grant is what a person allowed an OAuth client to do as one of their agents: Agent, until Expires, with
// Preset and Machines as the person chose them on the consent page: a ceiling of the grant's own, within
// what the agent may do (and the agent's owner's role), so that allowing another client never widens it.
// Refresh is the SHA-256 of its refresh token (none without offline_access), Spent those of the refresh
// tokens it replaced, and Code that of the authorization code it came from.
type Grant struct {
	ID         string    `json:"id"`
	Agent      string    `json:"agent"`
	Client     string    `json:"client"`
	ClientName string    `json:"client_name"`
	Scope      string    `json:"scope"`
	Preset     string    `json:"preset"`
	Machines   []string  `json:"machines,omitempty"`
	Created    time.Time `json:"created"`
	LastUsed   time.Time `json:"last_used,omitzero"`
	Expires    time.Time `json:"expires"`
	Refresh    string    `json:"refresh,omitempty"`
	Spent      []string  `json:"spent,omitempty"`
	Code       string    `json:"code,omitempty"`
}

// Issued is what a grant's token request gets: an access token, and a refresh token with offline_access.
type Issued struct {
	Access        string
	AccessExpires time.Time
	Refresh       string
	Grant         Grant
}

type accessRecord struct {
	Grant   string    `json:"grant"`
	Expires time.Time `json:"expires"`
}

func newSecretWith(prefix string) (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

// decodeKey reads the JSON value at key into v, reporting whether there was one to read.
func decodeKey(tx kv.Tx, bucket []byte, key []byte, v any) bool {
	raw := tx.Get(bucket, key)
	return raw != nil && json.Unmarshal(raw, v) == nil
}

// ---- clients

// RegisterClient stores a client registration and gives it an ID. Registrations never used for longer
// than ClientUnused are forgotten on the way; ErrTooMany when MaxClients are kept.
func (s *Store) RegisterClient(c OAuthClient) (OAuthClient, error) {
	id, err := randomID(18)
	if err != nil {
		return OAuthClient{}, err
	}
	now := s.now().UTC()
	c.ID, c.Created, c.Used = "lbc_"+id, now, time.Time{}
	err = s.db.Update(func(tx kv.Tx) error {
		var old [][]byte
		tx.Range(bucketClients, kv.Range{}, func(k, v []byte) bool {
			var x OAuthClient
			if json.Unmarshal(v, &x) != nil || (x.Used.IsZero() && now.Sub(x.Created) > ClientUnused) {
				old = append(old, append([]byte(nil), k...))
			}
			return true
		})
		for _, k := range old {
			_ = tx.Delete(bucketClients, k)
		}
		if tx.Count(bucketClients) >= MaxClients {
			return ErrTooMany
		}
		v, _ := json.Marshal(c)
		return tx.Put(bucketClients, []byte(c.ID), v)
	})
	if err != nil {
		return OAuthClient{}, err
	}
	return c, nil
}

// OAuthClient returns the registered client with this ID, unless it was never used and has lapsed.
func (s *Store) OAuthClient(id string) (OAuthClient, bool) {
	var c OAuthClient
	ok := false
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketClients, []byte(id)); v != nil {
			ok = json.Unmarshal(v, &c) == nil
		}
		return nil
	})
	if ok && c.Used.IsZero() && s.now().Sub(c.Created) > ClientUnused {
		return OAuthClient{}, false
	}
	return c, ok
}

// UseClient records that a registered client was used to sign in, so that it is kept.
func (s *Store) UseClient(id string) {
	_ = s.db.Update(func(tx kv.Tx) error {
		var c OAuthClient
		if !decodeKey(tx, bucketClients, []byte(id), &c) || !c.Used.IsZero() {
			return nil
		}
		c.Used = s.now().UTC()
		v, _ := json.Marshal(c)
		return tx.Put(bucketClients, []byte(id), v)
	})
}

// ---- grants

func grants(tx kv.Tx, match func(Grant) bool) []Grant {
	var out []Grant
	tx.Range(bucketGrants, kv.Range{}, func(_, v []byte) bool {
		var g Grant
		if json.Unmarshal(v, &g) == nil && (match == nil || match(g)) {
			out = append(out, g)
		}
		return true
	})
	return out
}

func putGrant(tx kv.Tx, g Grant) error {
	v, _ := json.Marshal(g)
	return tx.Put(bucketGrants, []byte(g.ID), v)
}

// deleteGrants deletes the grants that match, with their access tokens and the keys added with them.
func deleteGrants(tx kv.Tx, match func(Grant) bool) error {
	gone := map[string]bool{}
	for _, g := range grants(tx, match) {
		gone[g.ID] = true
		if err := tx.Delete(bucketGrants, []byte(g.ID)); err != nil {
			return err
		}
	}
	if len(gone) == 0 {
		return nil
	}
	for id := range gone { // the keys an agent added with this grant go with it
		if err := dropKeys(tx, "grant:"+id); err != nil {
			return err
		}
	}
	return deleteAccess(tx, func(a accessRecord) bool { return gone[a.Grant] })
}

func deleteAccess(tx kv.Tx, match func(accessRecord) bool) error {
	var keys [][]byte
	tx.Range(bucketAccess, kv.Range{}, func(k, v []byte) bool {
		var a accessRecord
		if json.Unmarshal(v, &a) != nil || match(a) {
			keys = append(keys, append([]byte(nil), k...))
		}
		return true
	})
	for _, k := range keys {
		if err := tx.Delete(bucketAccess, k); err != nil {
			return err
		}
	}
	return nil
}

// issue makes an access token for g, and a new refresh token if refresh, and stores them. Expired access
// tokens are forgotten on the way.
func (s *Store) issue(tx kv.Tx, g *Grant, refresh bool) (Issued, error) {
	now := s.now().UTC()
	access, err := newSecretWith(AccessPrefix)
	if err != nil {
		return Issued{}, err
	}
	out := Issued{Access: access, AccessExpires: now.Add(AccessTTL)}
	if g.Expires.Before(out.AccessExpires) {
		out.AccessExpires = g.Expires
	}
	if refresh {
		r, err := newSecretWith(RefreshPrefix)
		if err != nil {
			return Issued{}, err
		}
		out.Refresh, g.Refresh = r, hashToken(r)
	}
	if err := deleteAccess(tx, func(a accessRecord) bool { return !now.Before(a.Expires) }); err != nil {
		return Issued{}, err
	}
	v, _ := json.Marshal(accessRecord{Grant: g.ID, Expires: out.AccessExpires})
	if err := tx.Put(bucketAccess, []byte(hashToken(access)), v); err != nil {
		return Issued{}, err
	}
	if err := putGrant(tx, *g); err != nil {
		return Issued{}, err
	}
	out.Grant = *g
	return out, nil
}

// CreateGrant stores a new grant for an existing agent and issues its first tokens: an access token, and
// a refresh token when offline.
func (s *Store) CreateGrant(g Grant, offline bool) (Issued, error) {
	id, err := randomID(12)
	if err != nil {
		return Issued{}, err
	}
	now := s.now().UTC()
	g.ID, g.Created, g.LastUsed = id, now, now
	if !now.Before(g.Expires) {
		return Issued{}, errors.New("a grant must end in the future")
	}
	var out Issued
	err = s.db.Update(func(tx kv.Tx) error {
		if _, ok := getAgent(tx, g.Agent); !ok {
			return fmt.Errorf("%w: no agent %q", ErrNotFound, g.Agent)
		}
		out, err = s.issue(tx, &g, offline)
		return err
	})
	return out, err
}

// RefreshGrant uses a refresh token of client: it is replaced by a new one, with a new access token. A
// refresh token used again after it was replaced revokes its grant (ErrReplayed, with the grant that was
// revoked); an unknown or expired
// one, or another client's, is ErrBadGrant.
func (s *Store) RefreshGrant(refresh, client string) (Issued, error) {
	if !strings.HasPrefix(refresh, RefreshPrefix) {
		return Issued{}, ErrBadGrant
	}
	h := hashToken(refresh)
	now := s.now().UTC()
	var out Issued
	replayed := false
	var gone Grant
	err := s.db.Update(func(tx kv.Tx) error {
		var g Grant
		found := false
		for _, x := range grants(tx, nil) {
			if x.Refresh == h || slices.Contains(x.Spent, h) {
				g, found = x, true
			}
		}
		if !found || g.Client != client {
			return ErrBadGrant
		}
		if g.Refresh != h { // replaced already: someone else has it
			replayed, gone = true, g
			return deleteGrants(tx, func(x Grant) bool { return x.ID == g.ID })
		}
		if !now.Before(g.Expires) {
			_ = deleteGrants(tx, func(x Grant) bool { return x.ID == g.ID })
			return ErrBadGrant
		}
		if _, ok := agentPrincipal(tx, g.Agent); !ok { // the agent is gone, or its owner disabled
			return ErrBadGrant
		}
		g.Spent = append(g.Spent, h)
		if len(g.Spent) > keepSpent {
			g.Spent = g.Spent[len(g.Spent)-keepSpent:]
		}
		g.LastUsed = now
		var err error
		out, err = s.issue(tx, &g, true)
		return err
	})
	switch {
	case replayed && err == nil:
		return Issued{Grant: gone}, ErrReplayed
	case errors.Is(err, ErrBadGrant):
		return Issued{}, ErrBadGrant
	}
	return out, err
}

// Grants lists the grants of an agent ("": every agent's), the newest first.
func (s *Store) Grants(agent string) []Grant {
	var out []Grant
	_ = s.db.View(func(tx kv.Tx) error {
		out = grants(tx, func(g Grant) bool { return agent == "" || g.Agent == agent })
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Grant returns the grant with id.
func (s *Store) Grant(id string) (Grant, bool) {
	for _, g := range s.Grants("") {
		if g.ID == id {
			return g, true
		}
	}
	return Grant{}, false
}

// RevokeGrant revokes a grant with its tokens.
func (s *Store) RevokeGrant(id string) error {
	return s.db.Update(func(tx kv.Tx) error {
		if len(grants(tx, func(g Grant) bool { return g.ID == id })) == 0 {
			return ErrNotFound
		}
		return deleteGrants(tx, func(g Grant) bool { return g.ID == id })
	})
}

// RevokeCode revokes the grant made with the authorization code whose hash this is: the code was sent
// again, so someone else may have it. It reports the grant, if there was one.
func (s *Store) RevokeCode(codeHash string) (Grant, bool) {
	var gone Grant
	found := false
	_ = s.db.Update(func(tx kv.Tx) error {
		gs := grants(tx, func(g Grant) bool { return g.Code == codeHash })
		if len(gs) == 0 {
			return nil
		}
		gone, found = gs[0], true
		return deleteGrants(tx, func(g Grant) bool { return g.Code == codeHash })
	})
	return gone, found
}

// RevokeOAuthToken revokes an OAuth token of client (RFC 7009) and with it the whole grant, its access
// and refresh tokens alike: a client revoking any of them is signing out (Claude Code's logout sends its
// access token). It reports the grant; a token that is not the client's, or unknown, revokes nothing.
func (s *Store) RevokeOAuthToken(secret, client string) (Grant, bool) {
	h := hashToken(secret)
	var g Grant
	found := false
	_ = s.db.Update(func(tx kv.Tx) error {
		switch {
		case strings.HasPrefix(secret, AccessPrefix):
			var a accessRecord
			if !decodeKey(tx, bucketAccess, []byte(h), &a) {
				return nil
			}
			gs := grants(tx, func(x Grant) bool { return x.ID == a.Grant })
			if len(gs) == 0 || gs[0].Client != client {
				return nil
			}
			g, found = gs[0], true
			return deleteGrants(tx, func(x Grant) bool { return x.ID == a.Grant })
		case strings.HasPrefix(secret, RefreshPrefix):
			gs := grants(tx, func(x Grant) bool { return x.Refresh == h || slices.Contains(x.Spent, h) })
			if len(gs) == 0 || gs[0].Client != client {
				return nil
			}
			g, found = gs[0], true
			return deleteGrants(tx, func(x Grant) bool { return x.ID == gs[0].ID })
		}
		return nil
	})
	return g, found
}

// usedEvery is how often a grant's last use is written down.
const usedEvery = 5 * time.Minute

// verifyAccess resolves an access token to its grant's agent.
func (s *Store) verifyAccess(secret string) (Principal, bool) {
	h := hashToken(secret)
	now := s.now()
	var p Principal
	ok := false
	var g Grant
	_ = s.db.View(func(tx kv.Tx) error {
		var a accessRecord
		if !decodeKey(tx, bucketAccess, []byte(h), &a) || !now.Before(a.Expires) {
			return nil
		}
		gs := grants(tx, func(x Grant) bool { return x.ID == a.Grant })
		if len(gs) == 0 || !now.Before(gs[0].Expires) {
			return nil
		}
		g = gs[0]
		if p, ok = grantPrincipal(tx, g); ok {
			p.Expires = a.Expires
		}
		return nil
	})
	if ok && now.Sub(g.LastUsed) > usedEvery {
		_ = s.db.Update(func(tx kv.Tx) error {
			gs := grants(tx, func(x Grant) bool { return x.ID == g.ID })
			if len(gs) == 0 {
				return nil
			}
			gs[0].LastUsed = now.UTC()
			return putGrant(tx, gs[0])
		})
	}
	return p, ok
}
