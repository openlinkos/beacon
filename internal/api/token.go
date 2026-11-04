package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// The caller's own credential: GET /api/v1/token says who it is, what it may do and when it expires
// (whoami), and POST /api/v1/token/renew gives a token a new secret before it expires. Both are open to
// any credential: it acts on nothing but itself.

func init() {
	Routes = append(Routes, "GET /api/v1/token", "POST /api/v1/token/renew", "DELETE /api/v1/tokens/{name}")
}

func (s *Server) registerToken(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/token", s.withPrincipal("", s.whoami))
	mux.Handle("POST /api/v1/token/renew", s.withPrincipal("", s.renewToken))
	mux.Handle("DELETE /api/v1/tokens/{name}", s.with("machines:write", s.revokeToken))
}

// ScopeProblem says why p may not use scope ("a|b"): for an agent, what its preset within its owner's
// role allows, so that it knows whom to ask.
func ScopeProblem(p store.Principal, scope string) string {
	want := strings.ReplaceAll(scope, "|", " or ")
	if p.Agent == "" {
		return "the token lacks the " + want + " scope"
	}
	allowed := strings.Join(p.Scopes, ", ")
	if allowed == "" {
		allowed = "nothing"
	}
	return p.Name() + " may not use the " + want + " scope: its preset (" + p.Preset + ") within its owner's role allows " + allowed +
		"; its owner can change the preset on the console's Agents page"
}

func (s *Server) whoami(w http.ResponseWriter, _ *http.Request, p store.Principal) {
	v, err := s.Bridge.Whoami(p, "api")
	if err != nil {
		s.tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) renewToken(w http.ResponseWriter, r *http.Request, p store.Principal) {
	out, err := s.Bridge.RenewToken(r.Context(), p, "api")
	if err != nil {
		s.tokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// revokeToken revokes another token: one that reaches machines (machines:ssh), or an agent's, the pause
// switch for one agent. The tokens of machines and services stay with the admin socket
// (bridge.Revocable).
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	name := r.PathValue("name")
	err := s.Bridge.RevokeToken(r.Context(), who, name)
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
	case errors.Is(err, bridge.ErrTokenNotRenewable), errors.Is(err, bridge.ErrOAuthNotRenewable):
		writeError(w, http.StatusForbidden, "token.not_renewable", err.Error())
	case errors.Is(err, store.ErrNotFound): // revoked between the check and the call
		writeError(w, http.StatusUnauthorized, "auth.invalid", "the token is unknown or has expired")
	default:
		s.Log.Error("token operation failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}
