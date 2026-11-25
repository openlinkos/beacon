package api

import (
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/bridge"
)

func (s *Server) incidentEpisodes(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		var err error
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "bad_input", "limit is 1 to 200")
			return
		}
	}
	page, err := s.Bridge.IncidentEpisodes(r.Context(), bridge.IncidentEpisodeQuery{
		Since: q.Get("since"), Until: q.Get("until"), Before: q.Get("before"),
		Group: q.Get("group"), Severity: q.Get("severity"), Origin: q.Get("origin"), Limit: limit,
	})
	if err != nil {
		s.outcomeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
