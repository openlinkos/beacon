package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
)

func shareLinksPath(kind, id string) string {
	return consolePath("/" + kind + "/" + url.PathEscape(id) + "/share-links")
}

func (s *Server) shareLinksForm(group, kind string) func(http.ResponseWriter, *http.Request, *viewer) {
	return func(w http.ResponseWriter, r *http.Request, v *viewer) {
		if !v.owner() || !v.person() {
			s.forbidden(w, r, v, bridge.ShareScope)
			return
		}
		id := r.PathValue("id")
		actor := v.actor()
		target, err := s.Bridge.ShareTarget(actor, kind, id)
		if errors.Is(err, bridge.ErrForbidden) {
			s.forbidden(w, r, v, bridge.ShareScope)
			return
		}
		if err != nil {
			s.missing(w, r, v)
			return
		}
		show := func(address string, expires time.Time, problem string, code int) {
			s.renderShareLinks(w, r, v, group, id, target.Title, address, expires, problem, code)
		}
		if r.Method == http.MethodGet {
			show("", time.Time{}, "", http.StatusOK)
			return
		}
		switch r.PostForm.Get("intent") {
		case "create":
			var shared bridge.SharedLink
			shared, err = s.Bridge.CreateShareLink(r.Context(), actor, kind, id)
			if err == nil {
				show(shared.URL, shared.ExpiresAt, "", http.StatusOK)
				return
			}
		case "revoke":
			err = s.Bridge.RevokeShareLinks(r.Context(), actor, kind, id)
			if err == nil {
				back(w, r, shareLinksPath(group, id), "done", "shared_links_revoked")
				return
			}
		default:
			show("", time.Time{}, s.t("Choose to create a share link or revoke existing links."), http.StatusBadRequest)
			return
		}
		switch {
		case errors.Is(err, bridge.ErrForbidden):
			s.forbidden(w, r, v, bridge.ShareScope)
		case errors.Is(err, bridge.ErrShareNotFound):
			s.missing(w, r, v)
		default:
			s.log().Warn("console: share link operation failed", "err", err)
			show("", time.Time{}, s.t("Share links are unavailable. Try again."), http.StatusServiceUnavailable)
		}
	}
}
