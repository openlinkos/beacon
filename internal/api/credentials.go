package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

const teamPath = "/api/v1/team-credentials"

func (s *Server) registerCredentials(mux *http.ServeMux) {
	if s.Credentials == nil && !s.CredentialsConfigured {
		return
	}
	mux.Handle("GET "+teamPath, s.withPrincipal(secrets.UseScope, s.listTeamCredentials))
	mux.Handle("POST "+teamPath+"/{id}/use", s.withPrincipal(secrets.UseScope, s.useTeamCredential))
}

func (s *Server) listTeamCredentials(w http.ResponseWriter, _ *http.Request, p store.Principal) {
	if s.Credentials == nil {
		teamCredentialError(w, secrets.ErrState)
		return
	}
	out, err := s.Credentials.List(p)
	if err != nil {
		teamCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": out})
}

func (s *Server) useTeamCredential(w http.ResponseWriter, r *http.Request, p store.Principal) {
	if s.Credentials == nil {
		teamCredentialError(w, secrets.ErrState)
		return
	}
	// The request has no destination, headers or body from the caller.
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	body, err := io.ReadAll(r.Body)
	if err != nil || strings.TrimSpace(string(body)) != "" {
		writeError(w, http.StatusBadRequest, "bad_input", "this operation takes no body")
		return
	}
	out, err := s.Credentials.Use(r.Context(), p, "api", r.PathValue("id"))
	writeCredentialResult(w, http.StatusOK, out, err)
}

func writeCredentialResult(w http.ResponseWriter, code int, value any, err error) {
	if err != nil {
		teamCredentialError(w, err)
		return
	}
	writeJSON(w, code, value)
}

func teamCredentialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secrets.ErrAccess):
		writeError(w, http.StatusForbidden, "credential.denied", secrets.ErrAccess.Error())
	case errors.Is(err, secrets.ErrInput):
		writeError(w, http.StatusBadRequest, "bad_input", secrets.ErrInput.Error())
	case errors.Is(err, secrets.ErrUpstream):
		writeError(w, http.StatusBadGateway, "credential.upstream", secrets.ErrUpstream.Error())
	case errors.Is(err, secrets.ErrAnchorPending):
		writeError(w, http.StatusServiceUnavailable, "credential.anchor_pending", secrets.ErrAnchorPending.Error())
	default:
		writeError(w, http.StatusServiceUnavailable, "credential.unavailable", secrets.ErrState.Error())
	}
}
