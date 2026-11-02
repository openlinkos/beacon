package api

import (
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/bridge"
)

// The route for probing on demand: every location that probes a target probes it again at once, and the
// answer holds each location's result. The MCP tool probe_now calls the same bridge method.

func (s *Server) probeNow(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		AlertID string `json:"alert_id"`
		Target  string `json:"target"`
		Module  string `json:"module"`
		Service string `json:"service"`
		Wait    int    `json:"wait"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	out, err := s.Bridge.ProbeNow(r.Context(), who, bridge.ProbeNowInput{AlertID: in.AlertID, Target: in.Target, Module: in.Module, Service: in.Service, Wait: in.Wait})
	switch {
	case errors.Is(err, bridge.ErrProbeBusy):
		writeError(w, http.StatusTooManyRequests, "probe.busy", err.Error())
	case errors.Is(err, bridge.ErrProbeNotFound):
		writeError(w, http.StatusNotFound, "probe.not_found", err.Error())
	case errors.Is(err, bridge.ErrProbeUnavailable):
		writeError(w, http.StatusServiceUnavailable, "probe.unavailable", err.Error())
	case err != nil:
		s.opError(w, err)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}
