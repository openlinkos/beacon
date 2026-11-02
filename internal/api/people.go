package api

import (
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// People and the caller's SSH keys. Managing accounts needs accounts:write (an owner's): listing them,
// inviting people, changing roles, disabling, making password reset links. Any credential with
// machines:ssh manages its own SSH keys: an agent's are its agent's, shown to its owner.

func init() {
	Routes = append(Routes, "GET /api/v1/users", "POST /api/v1/users/invitations", "PATCH /api/v1/users/{name}", "POST /api/v1/users/{name}/reset",
		"GET /api/v1/ssh-keys", "POST /api/v1/ssh-keys", "DELETE /api/v1/ssh-keys")
}

func (s *Server) registerPeople(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/users", s.with("accounts:write", s.listUsers))
	mux.Handle("POST /api/v1/users/invitations", s.with("accounts:write", s.inviteUser))
	mux.Handle("PATCH /api/v1/users/{name}", s.with("accounts:write", s.updateUser))
	mux.Handle("POST /api/v1/users/{name}/reset", s.with("accounts:write", s.resetUser))
	mux.Handle("GET /api/v1/ssh-keys", s.with("machines:ssh", s.listKeys))
	mux.Handle("POST /api/v1/ssh-keys", s.with("machines:ssh", s.addKey))
	mux.Handle("DELETE /api/v1/ssh-keys", s.with("machines:ssh", s.removeKey))
}

// peopleError maps the operations' errors on people and keys to HTTP answers; notFound is the code for
// a person or a key that does not exist.
func (s *Server) peopleError(w http.ResponseWriter, err error, notFound string) {
	switch {
	case errors.Is(err, bridge.ErrForbidden):
		writeError(w, http.StatusForbidden, "auth.scope", err.Error())
	case errors.Is(err, store.ErrLastOwner):
		writeError(w, http.StatusConflict, "user.last_owner", err.Error())
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "key.exists", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, notFound, err.Error())
	case errors.Is(err, bridge.ErrBadInput):
		writeError(w, http.StatusBadRequest, "bad_input", err.Error())
	default:
		s.Log.Error("people operation failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) listUsers(w http.ResponseWriter, _ *http.Request, who bridge.Actor) {
	users, invs, err := s.Bridge.Users(who)
	if err != nil {
		s.peopleError(w, err, "user.not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "invitations": invs})
}

func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		Role string `json:"role"`
		Note string `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	l, err := s.Bridge.Invite(r.Context(), who, in.Role, in.Note)
	if err != nil {
		s.peopleError(w, err, "user.not_found")
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in bridge.UserChange
	if !readJSON(w, r, &in) {
		return
	}
	u, err := s.Bridge.UpdateUser(r.Context(), who, r.PathValue("name"), in)
	if err != nil {
		s.peopleError(w, err, "user.not_found")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) resetUser(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	l, err := s.Bridge.ResetLink(r.Context(), who, r.PathValue("name"))
	if err != nil {
		s.peopleError(w, err, "user.not_found")
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) listKeys(w http.ResponseWriter, _ *http.Request, who bridge.Actor) {
	keys, err := s.Bridge.SSHKeys(who, "")
	if err != nil {
		s.peopleError(w, err, "key.not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) addKey(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		Key     string `json:"key"`
		Comment string `json:"comment"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	k, err := s.Bridge.AddSSHKey(r.Context(), who, "", in.Key, in.Comment)
	if err != nil {
		s.peopleError(w, err, "key.not_found")
		return
	}
	writeJSON(w, http.StatusCreated, k)
}

func (s *Server) removeKey(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	fp := r.URL.Query().Get("fingerprint")
	if fp == "" {
		writeError(w, http.StatusBadRequest, "bad_input", "give the key's fingerprint: ?fingerprint=SHA256:…")
		return
	}
	if err := s.Bridge.RemoveSSHKey(r.Context(), who, "", fp); err != nil {
		s.peopleError(w, err, "key.not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fingerprint": fp})
}
