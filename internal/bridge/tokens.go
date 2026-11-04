package bridge

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// The caller's own credential: what it may do (whoami), and renewing a token before it expires, so that
// agents' credentials are handled by Beacon and nobody hands out long-lived tokens by hand
// (docs/guide/on-call.md). An OAuth access token is not renewed: the client that holds it refreshes it.

// Token errors the API and MCP turn into answers.
var (
	ErrTokenTooSoon      = store.ErrTooSoon
	ErrTokenNotRenewable = store.ErrNotRenewable
	// ErrOAuthNotRenewable means the caller came with an OAuth access token, which its client refreshes.
	ErrOAuthNotRenewable = errors.New("an OAuth access token is refreshed by the client that holds it; there is nothing to renew")
)

// TokenView is the caller's credential as its holder sees it: never the secret or its hash. Actor is how
// the audit log names the caller; User and Agent say whose it is (an agent's, owned by a person), Preset
// what the agent was given, and Scopes what it may do now (the preset within its owner's role).
// Credential is token, or oauth for an access token a person allowed an OAuth client (Client) to hold.
type TokenView struct {
	Name       string    `json:"name"`
	Actor      string    `json:"actor"`
	User       string    `json:"user,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Preset     string    `json:"preset,omitempty"`
	Credential string    `json:"credential"`
	Client     string    `json:"client,omitempty"`
	Scopes     []string  `json:"scopes"`
	Subject    string    `json:"subject,omitempty"`
	Machines   []string  `json:"machines,omitempty"` // the machines it reaches through the SSH channel; none: all
	SSHKeys    []string  `json:"ssh_keys,omitempty"` // fingerprints of the keys it signs in with
	Created    time.Time `json:"created,omitzero"`
	Expires    time.Time `json:"expires"`
	// Lifetime is what the token was created with, as `token create -expires` takes it (365d, 12h); a
	// renewal makes it expire this long from then.
	Lifetime string    `json:"lifetime"`
	Renewed  time.Time `json:"renewed,omitzero"`
	// Renewable is false for an enrolled machine's token, which enroll -rotate replaces, and for an OAuth
	// access token. RenewableAt is when it may next renew itself (at most once a day).
	Renewable   bool      `json:"renewable"`
	RenewableAt time.Time `json:"renewable_at,omitzero"`
	Via         string    `json:"via"` // how the caller reached Beacon: api or mcp
}

// Renewed is the answer to a renewal: the new secret, shown once, and when the old one stops working.
type Renewed struct {
	Token         TokenView `json:"token"`
	Secret        string    `json:"secret"`
	OldValidUntil time.Time `json:"old_secret_valid_until"`
}

// credentialView describes the principal p, which came with the token t (zero for an OAuth access token).
func (b *Bridge) credentialView(p store.Principal, t store.Token, via string) TokenView {
	v := TokenView{Name: p.Token, Actor: p.Name(), User: p.User, Agent: p.Agent, Preset: p.Preset, Credential: "token",
		Scopes: slices.Clone(p.Scopes), Subject: p.Subject, Machines: p.Machines, Expires: p.Expires, Via: via}
	if p.Grant != "" {
		v.Name, v.Credential, v.Client, v.Lifetime = p.Agent, "oauth", p.Client, store.FormatTTL(store.AccessTTL)
		if g, ok := b.d.Store.Grant(p.Grant); ok {
			v.Created = g.Created
		}
	} else {
		v.Created, v.Lifetime, v.Renewed, v.Renewable = t.Created, store.FormatTTL(t.Lifetime), t.Renewed, t.Renewable()
		if v.Renewable {
			v.RenewableAt = t.RenewableAt()
		}
	}
	keys := t.SSHKeys
	if p.Agent != "" {
		if a, ok := b.d.Store.Agent(p.Agent); ok {
			keys = a.SSHKeys
		}
	}
	for _, k := range keys {
		v.SSHKeys = append(v.SSHKeys, k.Fingerprint)
	}
	return v
}

// Whoami describes the caller's credential, so that an agent can learn who it is to Beacon, what it may
// do and when its credential expires.
func (b *Bridge) Whoami(p store.Principal, via string) (TokenView, error) {
	if p.Grant != "" {
		return b.credentialView(p, store.Token{}, via), nil
	}
	t, ok := b.token(p.Token)
	if !ok {
		return TokenView{}, store.ErrNotFound
	}
	return b.credentialView(p, t, via), nil
}

// RenewToken gives the caller's token a new secret that expires its lifetime from now; the old secret
// keeps working for store.RenewGrace. At most once every store.RenewEvery; never for an enrolled
// machine's token or an OAuth access token. Audited and echoed to the chat, as every write is.
func (b *Bridge) RenewToken(ctx context.Context, p store.Principal, via string) (Renewed, error) {
	if p.Grant != "" || p.Token == "" {
		return Renewed{}, ErrOAuthNotRenewable
	}
	secret, t, err := b.d.Store.RenewToken(p.Token)
	if err != nil {
		return Renewed{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: p.Name(), Via: via, Action: "token_renew", Target: t.Name,
		Detail: "expires " + t.Expires.UTC().Format(time.DateOnly) + "; the old secret works until " + t.OldUntil.UTC().Format(time.RFC3339)})
	b.echo(ctx, b.t("Token %s renewed itself; expires %s", t.Name, t.Expires.In(b.d.Config.Location).Format(time.DateOnly)))
	p.Expires = t.Expires
	return Renewed{Token: b.credentialView(p, t, via), Secret: secret, OldValidUntil: t.OldUntil}, nil
}

// token finds a token by name.
func (b *Bridge) token(name string) (store.Token, bool) {
	for _, t := range b.d.Store.Tokens() {
		if t.Name == name {
			return t, true
		}
	}
	return store.Token{}, false
}
