package api

import (
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// The caller's own token: GET /api/v1/token says what it may do and when it expires (whoami), and
// POST /api/v1/token/renew gives it a new secret before it expires. Both are open to any token: the
// token itself is the credential, and it acts on nothing but itself.

func init() {
	Routes = append(Routes, "GET /api/v1/token", "POST /api/v1/token/renew", "DELETE /api/v1/tokens/{name}")
}

func (s *Server) registerToken(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/token", s.withToken("", s.whoami))
	mux.Handle("POST /api/v1/token/renew", s.withToken("", s.renewToken))
	mux.Handle("DELETE /api/v1/tokens/{name}", s.withToken("machines:write", s.revokeToken))
}

func (s *Server) whoami(w http.ResponseWriter, _ *http.Request, t store.Token) {
	v, err := s.Bridge.Whoami(bridge.Actor{Name: t.Name, Via: "api"})
	if err != nil {
		s.tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) renewToken(w http.ResponseWriter, r *http.Request, t store.Token) {
	out, err := s.Bridge.RenewToken(r.Context(), bridge.Actor{Name: t.Name, Via: "api"})
	if err != nil {
		s.tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// revokeToken revokes another token: one that reaches machines (machines:ssh), the pause switch for
// one agent. The tokens of machines and services stay with the admin socket (bridge.Revocable).
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, t store.Token) {
	name := r.PathValue("name")
	err := s.Bridge.RevokeToken(r.Context(), bridge.Actor{Name: t.Name, Via: "api"}, name)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "token.not_found", err.Error())
	case errors.Is(err, bridge.ErrBadInput):
		writeError(w, http.StatusForbidden, "token.not_revocable", err.Error())
	default:
		s.Log.Error("revoke token failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

// tokenError maps the token operations' errors to HTTP answers.
func (s *Server) tokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bridge.ErrTokenTooSoon):
		writeError(w, http.StatusTooManyRequests, "token.too_soon", err.Error())
	case errors.Is(err, bridge.ErrTokenNotRenewable):
		writeError(w, http.StatusForbidden, "token.not_renewable", err.Error())
	case errors.Is(err, store.ErrNotFound): // revoked between the check and the call
		writeError(w, http.StatusUnauthorized, "auth.invalid", "the token is unknown or has expired")
	default:
		s.Log.Error("token operation failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}
