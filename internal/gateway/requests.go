package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// Probing on demand: the two routes a probe agent uses beside its cycle. GET /api/v1/agent/requests
// long-polls for the requests whose targets this location has assigned, in the shape of its targets;
// POST /api/v1/agent/requests/{id} takes what it found. Both need probe:write and an active location,
// like the other probe routes. A gateway without a board (Requests nil) answers 404 as an older gateway
// would, and the probe asks again in an hour.

const (
	defaultWait = 25 * time.Second
	maxWait     = 55 * time.Second // under the server's write timeout
	maxAnswer   = 1 << 20          // bytes
)

var requestID = regexp.MustCompile(`^[a-f0-9]{8,64}$`)

func (g *gateway) requests(w http.ResponseWriter, r *http.Request, t store.Token) {
	if g.refuseStandby(w) {
		return
	}
	if g.Requests == nil {
		fail(w, http.StatusNotFound, "not_found", "no probing on demand on this gateway")
		return
	}
	_, l, ok := g.active(w, t)
	if !ok {
		return
	}
	wait := defaultWait
	if s := r.URL.Query().Get("wait"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			fail(w, http.StatusBadRequest, "bad_input", "wait is a duration such as 25s")
			return
		}
		wait = min(d, maxWait)
	}
	reqs := g.Requests.Wait(r.Context(), l.ID, wait)
	if reqs == nil {
		reqs = []targets.Request{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(reqs)
}

func (g *gateway) answer(w http.ResponseWriter, r *http.Request, t store.Token) {
	if g.refuseStandby(w) {
		return
	}
	if g.Requests == nil {
		fail(w, http.StatusNotFound, "not_found", "no probing on demand on this gateway")
		return
	}
	_, l, ok := g.active(w, t)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !requestID.MatchString(id) {
		fail(w, http.StatusNotFound, "request.not_found", "no such request")
		return
	}
	var in targets.Answer
	if err := json.NewDecoder(io.LimitReader(r.Body, maxAnswer)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad_input", "the body must be a JSON object with results")
		return
	}
	if err := g.Requests.Answer(id, l.ID, in.Results); err != nil {
		if errors.Is(err, probenow.ErrNotFound) {
			fail(w, http.StatusNotFound, "request.not_found", "no such request, or it has expired")
			return
		}
		fail(w, http.StatusBadRequest, "bad_input", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
