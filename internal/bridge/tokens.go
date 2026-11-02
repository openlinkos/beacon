package bridge

import (
	"context"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// The caller's own token: what it may do (whoami), and renewing it before it expires, so that agents'
// credentials are handled by Beacon and nobody hands out long-lived tokens by hand (docs/agents.md).

// Token errors the API and MCP turn into answers.
var (
	ErrTokenTooSoon      = store.ErrTooSoon
	ErrTokenNotRenewable = store.ErrNotRenewable
)

// TokenView is a token as its holder sees it: never the secret or its hash.
type TokenView struct {
	Name     string    `json:"name"`
	Scopes   []string  `json:"scopes"`
	Subject  string    `json:"subject,omitempty"`
	Machines []string  `json:"machines,omitempty"` // the machines it reaches through the SSH channel; none: all
	SSHKeys  []string  `json:"ssh_keys,omitempty"` // fingerprints of the keys it signs in with
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	// Lifetime is what the token was created with, as `token create -expires` takes it (365d, 12h); a
	// renewal makes it expire this long from then.
	Lifetime string    `json:"lifetime"`
	Renewed  time.Time `json:"renewed,omitzero"`
	// Renewable is false for an enrolled machine's token, which enroll -rotate replaces. RenewableAt is
	// when it may next renew itself (at most once a day).
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

func tokenView(t store.Token, via string) TokenView {
	v := TokenView{Name: t.Name, Scopes: t.Scopes, Subject: t.Subject, Machines: t.Machines, Created: t.Created, Expires: t.Expires,
		Lifetime: store.FormatTTL(t.Lifetime), Renewed: t.Renewed, Renewable: t.Renewable(), Via: via}
	if v.Renewable {
		v.RenewableAt = t.RenewableAt()
	}
	for _, k := range t.SSHKeys {
		v.SSHKeys = append(v.SSHKeys, k.Fingerprint)
	}
	return v
}

// Whoami describes the caller's token, so that an agent can learn what it may do and when it expires.
func (b *Bridge) Whoami(who Actor) (TokenView, error) {
	t, ok := b.token(who.Name)
	if !ok {
		return TokenView{}, store.ErrNotFound
	}
	return tokenView(t, who.Via), nil
}

// RenewToken gives the caller's token a new secret that expires its lifetime from now; the old secret
// keeps working for store.RenewGrace. At most once every store.RenewEvery; never for an enrolled
// machine's token. Audited and echoed to the chat, as every write is.
func (b *Bridge) RenewToken(ctx context.Context, who Actor) (Renewed, error) {
	secret, t, err := b.d.Store.RenewToken(who.Name)
	if err != nil {
		return Renewed{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "token_renew", Target: t.Name,
		Detail: "expires " + t.Expires.UTC().Format(time.DateOnly) + "; the old secret works until " + t.OldUntil.UTC().Format(time.RFC3339)})
	b.echo(ctx, b.t("Token %s renewed itself; expires %s", t.Name, t.Expires.In(b.d.Config.Location).Format(time.DateOnly)))
	return Renewed{Token: tokenView(t, who.Via), Secret: secret, OldValidUntil: t.OldUntil}, nil
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
