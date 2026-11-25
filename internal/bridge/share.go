package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

var (
	ErrShareNotFound = errors.New("no such alert or session")
	ErrShareDisabled = errors.New("signed page links are unavailable")
)

const ShareScope = "accounts:write"

// ShareTarget is the record whose outstanding signed links an owner can manage.
type ShareTarget struct {
	Kind, ID, Title string
	path            string
}

// SharedLink is shown to the caller that explicitly requests the current link.
type SharedLink struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ShareTarget returns the current target, without issuing a credential.
func (b *Bridge) ShareTarget(who Actor, kind, id string) (ShareTarget, error) {
	if !b.admin(who) {
		return ShareTarget{}, ErrForbidden
	}
	switch kind {
	case "alert":
		if g, ok := b.d.Store.Group(id); ok {
			return ShareTarget{Kind: kind, ID: id, Title: b.title(&g), path: link.IncidentPath(id, g.Episode)}, nil
		}
	case "session":
		if r, ok := b.d.Store.Session(id); ok {
			return ShareTarget{Kind: kind, ID: id, Title: r.Machine, path: link.SessionPath(id)}, nil
		}
	default:
		return ShareTarget{}, ErrBadInput
	}
	return ShareTarget{}, ErrShareNotFound
}

// CreateShareLink issues a signed URL for the current alert episode or the selected session.
func (b *Bridge) CreateShareLink(ctx context.Context, who Actor, kind, id string) (SharedLink, error) {
	target, err := b.ShareTarget(who, kind, id)
	if err != nil {
		return SharedLink{}, err
	}
	if kind == "session" && (who.Via != "cli" || who.Key != "") {
		p, ok := b.caller(who)
		r, found := b.d.Store.Session(id)
		access := b.d.Store.SessionAccess(p)
		if !ok || !found || !access.Details() || !access.Visible(r) {
			return SharedLink{}, ErrShareNotFound
		}
	}
	address := b.d.Pages.URL(target.path)
	u, err := url.Parse(address)
	if err != nil || address == "" {
		return SharedLink{}, ErrShareDisabled
	}
	expires, ok := b.d.Pages.Signer.Verify(http.MethodGet, target.path, u.Query().Get(link.Param))
	if !ok {
		return SharedLink{}, ErrShareDisabled
	}
	entry := store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "create_page_link", Target: kind + "/" + id}
	if err := b.d.Store.Audit(entry); err != nil {
		return SharedLink{}, err
	}
	b.echo(ctx, b.t("%s created a share link for %s via %s", who.Name, entry.Target, b.viaName(who.Via)))
	return SharedLink{URL: address, ExpiresAt: expires.UTC()}, nil
}

// RevokeShareLinks invalidates every outstanding signed URL for this alert group or session.
func (b *Bridge) RevokeShareLinks(ctx context.Context, who Actor, kind, id string) error {
	if _, err := b.ShareTarget(who, kind, id); err != nil {
		return err
	}
	if _, err := b.d.Store.RevokePageLinks(kind, id, store.AuditEntry{Actor: who.Name, Via: who.Via}); err != nil {
		return err
	}
	b.echo(ctx, b.t("%s revoked share links for %s via %s", who.Name, kind+"/"+id, b.viaName(who.Via)))
	return nil
}
