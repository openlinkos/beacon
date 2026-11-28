package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/store"
)

// Agents. Any credential may call; the bridge decides who may manage whose agent. Custom scopes need
// accounts:write from a person or a legacy token, never from an agent, and stay within the owner's role
// and within what the caller itself may do.

func init() {
	Routes = append(Routes, "GET /api/v1/agents", "POST /api/v1/agents", "GET /api/v1/agents/{name}", "PATCH /api/v1/agents/{name}")
}

func (s *Server) registerAgents(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/agents", s.with("", s.listAgents))
	mux.Handle("POST /api/v1/agents", s.with("", s.createAgent))
	mux.Handle("GET /api/v1/agents/{name}", s.with("", s.getAgent))
	mux.Handle("PATCH /api/v1/agents/{name}", s.with("", s.updateAgent))
}

type agentBody struct {
	Name     string    `json:"name"`
	Owner    string    `json:"owner"`
	Preset   *string   `json:"preset"`
	Scopes   []string  `json:"scopes"`
	Machines *[]string `json:"machines"`
	Expires  string    `json:"expires"`
}

// agentError maps an agent operation's error. notFound is the code for an agent that does not exist;
// empty means a missing name on create is bad input (a 404 on POST is not a refusal).
func (s *Server) agentError(w http.ResponseWriter, err error, notFound string) {
	switch {
	case errors.Is(err, bridge.ErrForbidden):
		writeError(w, http.StatusForbidden, "auth.scope", err.Error())
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "agent.exists", err.Error())
	case errors.Is(err, store.ErrNotFound):
		if notFound == "" {
			writeError(w, http.StatusBadRequest, "bad_input", err.Error())
			return
		}
		writeError(w, http.StatusNotFound, notFound, err.Error())
	case errors.Is(err, bridge.ErrBadInput):
		writeError(w, http.StatusBadRequest, "bad_input", err.Error())
	case errors.Is(err, bridge.ErrUnrecorded):
		writeError(w, http.StatusServiceUnavailable, "audit.not_recorded", err.Error())
	default:
		s.Log.Error("agent operation failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	agents, err := s.Bridge.Agents(who, strings.TrimSpace(r.URL.Query().Get("owner")))
	if err != nil {
		s.agentError(w, err, "agent.not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	v, err := s.Bridge.Agent(who, r.PathValue("name"))
	if err != nil {
		s.agentError(w, err, "agent.not_found")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in agentBody
	if !readJSON(w, r, &in) {
		return
	}
	var ttl time.Duration
	if in.Expires != "" {
		d, err := ctl.ParseTTL(in.Expires)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_input", err.Error())
			return
		}
		ttl = d
	}
	preset := ""
	if in.Preset != nil {
		preset = *in.Preset
	}
	agent := bridge.AgentInput{Name: strings.TrimSpace(in.Name), Owner: in.Owner, Preset: preset, TTL: ttl}
	if in.Machines != nil {
		agent.Machines = *in.Machines
	}
	if preset == store.PresetCustom {
		agent.Scopes = in.Scopes
		if agent.Scopes == nil {
			agent.Scopes = []string{}
		}
	}
	out, err := s.Bridge.CreateAgent(r.Context(), who, agent)
	if err != nil {
		s.agentError(w, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in agentBody
	if !readJSON(w, r, &in) {
		return
	}
	v, err := s.Bridge.UpdateAgent(r.Context(), who, r.PathValue("name"), bridge.AgentUpdate{Preset: in.Preset, Scopes: in.Scopes, Machines: in.Machines})
	if err != nil {
		s.agentError(w, err, "agent.not_found")
		return
	}
	writeJSON(w, http.StatusOK, v)
}
