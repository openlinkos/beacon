package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

func (s *Server) incidentOutcomes(w http.ResponseWriter, r *http.Request, p store.Principal) {
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		var err error
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "bad_input", "limit is 1 to 200")
			return
		}
	}
	out, err := s.Bridge.IncidentOutcomesFor(p, bridge.OutcomeQuery{
		Group: q.Get("group"), Before: q.Get("before"), Severity: q.Get("severity"),
		Since: q.Get("since"), Origin: q.Get("origin"), Evidence: q.Get("evidence"), Limit: limit,
	})
	if err != nil {
		s.outcomeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) incidentOutcome(w http.ResponseWriter, r *http.Request, p store.Principal) {
	out, err := s.Bridge.IncidentOutcomeFor(p, r.PathValue("id"))
	if err != nil {
		s.outcomeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) outcomeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bridge.ErrOutcomeNotFound):
		writeError(w, http.StatusNotFound, "incident.not_found", err.Error())
	case errors.Is(err, bridge.ErrBadInput):
		s.opError(w, err)
	default:
		s.Log.Error("read incident outcomes", "err", err)
		writeError(w, http.StatusInternalServerError, "incident.unavailable", "incident outcomes cannot be read")
	}
}
