package api

import (
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/bridge"
)

// The routes for working an alert: its evidence in one answer, its timeline, notes on it, and the
// change events around it.

func (s *Server) evidence(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	ev, err := s.Bridge.Evidence(r.Context(), r.PathValue("id"), r.URL.Query().Get("before"))
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	tl, err := s.Bridge.Timeline(r.Context(), r.PathValue("id"), r.URL.Query().Get("before"))
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tl)
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	g, err := s.Bridge.AddNote(r.Context(), r.PathValue("id"), who, in.Text)
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	q := r.URL.Query()
	in := bridge.ChangeQuery{Service: q.Get("service"), Machine: q.Get("machine"), Source: q.Get("source"), Since: q.Get("since")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "bad_input", "limit is 1 to 1000")
			return
		}
		in.Limit = n
	}
	cs, err := s.Bridge.Changes(in)
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": cs})
}
