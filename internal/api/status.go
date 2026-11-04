package api

import (
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/status"
)

// The public status pages for agents: a page's state as its public JSON has it, and the maintenance
// windows the pages announce. The page definitions are configuration (/api/v1/config/status_pages).

func init() {
	Routes = append(Routes, "GET /api/v1/status/{name}",
		"GET /api/v1/maintenance", "POST /api/v1/maintenance", "DELETE /api/v1/maintenance/{id}")
}

func (s *Server) registerStatus(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/status/{name}", s.with("status:read", s.statusPage))
	mux.Handle("GET /api/v1/maintenance", s.with("status:read", s.listMaintenance))
	mux.Handle("POST /api/v1/maintenance", s.with("maintenance:write", s.scheduleMaintenance))
	mux.Handle("DELETE /api/v1/maintenance/{id}", s.with("maintenance:write", s.endMaintenance))
}

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	if s.Status == nil {
		writeError(w, http.StatusNotFound, "status.not_found", "no status pages are served")
		return
	}
	p, err := s.Status.Page(r.Context(), r.PathValue("name"), r.URL.Query().Get("lang"))
	if errors.Is(err, status.ErrNotFound) {
		writeError(w, http.StatusNotFound, "status.not_found", "no status page with this name; get_config with kind status_pages lists them")
		return
	}
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) listMaintenance(w http.ResponseWriter, _ *http.Request, _ bridge.Actor) {
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": s.Bridge.MaintenanceWindows()})
}

func (s *Server) scheduleMaintenance(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in bridge.MaintenanceInput
	if !readJSON(w, r, &in) {
		return
	}
	m, err := s.Bridge.ScheduleMaintenance(r.Context(), who, in)
	if err != nil {
		s.maintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) endMaintenance(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	m, err := s.Bridge.EndMaintenance(r.Context(), r.PathValue("id"), who)
	if err != nil {
		s.maintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) maintenanceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bridge.ErrNoMaintenance):
		writeError(w, http.StatusNotFound, "maintenance.not_found", "no maintenance window with this id")
	case errors.Is(err, bridge.ErrMaintenanceEnded):
		writeError(w, http.StatusConflict, "maintenance.ended", err.Error())
	default:
		s.opError(w, err)
	}
}
