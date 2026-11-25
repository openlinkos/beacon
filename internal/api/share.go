package api

import (
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/bridge"
)

func (s *Server) registerShareLinks(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/share-links/{kind}/{id}", s.with(bridge.ShareScope, s.createShareLink))
	mux.Handle("POST /api/v1/share-links/{kind}/{id}/revoke", s.with(bridge.ShareScope, s.revokeShareLinks))
}

func (s *Server) createShareLink(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	shared, err := s.Bridge.CreateShareLink(r.Context(), who, r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		s.shareError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, shared)
}

func (s *Server) revokeShareLinks(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	if err := s.Bridge.RevokeShareLinks(r.Context(), who, r.PathValue("kind"), r.PathValue("id")); err != nil {
		s.shareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) shareError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bridge.ErrShareNotFound):
		writeError(w, http.StatusNotFound, "share.not_found", err.Error())
	case errors.Is(err, bridge.ErrShareDisabled):
		writeError(w, http.StatusServiceUnavailable, "share.unavailable", err.Error())
	case errors.Is(err, bridge.ErrForbidden):
		writeError(w, http.StatusForbidden, "auth.scope", "a standalone credential with accounts:write is required")
	case errors.Is(err, bridge.ErrBadInput):
		writeError(w, http.StatusBadRequest, "bad_input", "kind is alert or session")
	default:
		s.Log.Error("share link operation failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "share.unavailable", "share links are unavailable")
	}
}
