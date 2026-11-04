package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/settings"
)

// The settings' routes: every knob the secrets work offers, read whole or one by one, changed one
// at a time with a reason, reset to what the level above gives, and the history of changes with
// revert. The same operations are the MCP configuration tools' settings kind and the console's
// settings page, through the same service.

func settingsRoutes() []string {
	return []string{
		"GET /api/v1/settings", "GET /api/v1/settings/{key}", "PUT /api/v1/settings/{key}", "DELETE /api/v1/settings/{key}",
		"GET /api/v1/settings/history", "GET /api/v1/settings/history/{version}", "POST /api/v1/settings/history/{version}/revert",
	}
}

func init() { Routes = append(Routes, settingsRoutes()...) }

// settingsRead is who may read the settings: anyone who may read the status, and whoever may
// change them.
const settingsRead = "status:read|secrets:admin"

func (s *Server) registerSettings(mux *http.ServeMux) {
	if s.Settings == nil {
		return
	}
	mux.Handle("GET /api/v1/settings", s.with(settingsRead, s.listSettings))
	mux.Handle("GET /api/v1/settings/history", s.with(settingsRead, s.settingsHistory))
	mux.Handle("GET /api/v1/settings/history/{version}", s.with(settingsRead, s.settingsChange))
	mux.Handle("POST /api/v1/settings/history/{version}/revert", s.with("secrets:admin", s.revertSetting))
	mux.Handle("GET /api/v1/settings/{key}", s.with(settingsRead, s.getSetting))
	mux.Handle("PUT /api/v1/settings/{key}", s.with("secrets:admin", s.putSetting))
	mux.Handle("DELETE /api/v1/settings/{key}", s.with("secrets:admin", s.deleteSetting))
}

func settingsActor(who bridge.Actor) settings.Actor {
	return settings.Actor{Name: who.Name, Via: who.Via}
}

// settingsError maps the settings' errors to HTTP answers.
func (s *Server) settingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, settings.ErrNotFound):
		writeError(w, http.StatusNotFound, "settings.not_found", err.Error())
	case errors.Is(err, settings.ErrUnknown):
		writeError(w, http.StatusNotFound, "settings.unknown", err.Error())
	case errors.Is(err, settings.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "settings.invalid", err.Error())
	case errors.Is(err, settings.ErrConflict):
		writeError(w, http.StatusConflict, "settings.conflict", err.Error())
	case errors.Is(err, settings.ErrBadInput):
		writeError(w, http.StatusBadRequest, "bad_input", err.Error())
	default:
		s.Log.Error("settings change failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

// selector reads the query parameters that place a setting: collection, tag (more than one),
// entitlement and request.
func selectorOf(r *http.Request) settings.Selector {
	return settings.Selector{Collection: r.URL.Query().Get("collection"), Tags: r.URL.Query()["tag"],
		Entitlement: r.URL.Query().Get("entitlement"), Request: r.URL.Query().Get("request")}
}

func (s *Server) listSettings(w http.ResponseWriter, _ *http.Request, _ bridge.Actor) {
	writeJSON(w, http.StatusOK, s.Settings.List())
}

func (s *Server) getSetting(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	d, ok := settings.Get(r.PathValue("key"))
	if !ok {
		writeError(w, http.StatusNotFound, "settings.unknown", "no setting called "+r.PathValue("key"))
		return
	}
	res, levels, err := s.Settings.Value(d.Key, selectorOf(r))
	if err != nil {
		s.settingsError(w, err)
		return
	}
	v := settings.ViewOf(d, res)
	if len(levels) > 1 {
		v.Levels = []settings.Level{}
		for _, l := range levels {
			v.Levels = append(v.Levels, settings.Level{From: l.From, Target: l.Target, Value: l.Value})
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// putSettingRequest is what a change carries: the value, where it sits (the system scope unless
// said), why, and the version the caller last saw.
type putSettingRequest struct {
	Value     json.RawMessage `json:"value"`
	Scope     string          `json:"scope,omitempty"`
	Target    string          `json:"target,omitempty"`
	Reason    string          `json:"reason"`
	IfVersion int64           `json:"if_version,omitempty"`
}

func (s *Server) putSetting(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in putSettingRequest
	if !readBody(w, r, &in, 1<<20) {
		return
	}
	if in.Scope == "" {
		in.Scope = settings.ScopeSystem
	}
	ch, err := s.Settings.Set(r.Context(), settingsActor(who), in.Scope, in.Target, r.PathValue("key"), in.Value, in.Reason, in.IfVersion)
	if err != nil {
		s.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) deleteSetting(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	scope, target := settings.ScopeSystem, ""
	if sc := r.URL.Query().Get("scope"); sc != "" {
		scope, target = sc, r.URL.Query().Get("target")
	}
	ifv, ok := ifVersion(w, r)
	if !ok {
		return
	}
	ch, err := s.Settings.Reset(r.Context(), settingsActor(who), scope, target, r.PathValue("key"), r.URL.Query().Get("reason"), ifv)
	if err != nil {
		s.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) settingsHistory(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	limit := 20
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, 200)
	}
	before := int64(0)
	if n, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
		before = n
	}
	list := s.Settings.History(r.URL.Query().Get("key"), r.URL.Query().Get("scope"), r.URL.Query().Get("target"), before, limit)
	if list == nil {
		list = []settings.Change{}
	}
	writeJSON(w, http.StatusOK, struct {
		Changes []settings.Change `json:"changes"`
	}{Changes: list})
}

func (s *Server) settingsChange(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "bad_input", "the version is a whole number, as the history lists it")
		return
	}
	ch, ok := s.Settings.ChangeAt(version)
	if !ok {
		writeError(w, http.StatusNotFound, "settings.not_found", "no settings change with this version")
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) revertSetting(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "bad_input", "the version is a whole number, as the history lists it")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 && !readBody(w, r, &in, 4<<10) {
		return
	}
	ch, err := s.Settings.Revert(r.Context(), settingsActor(who), version, in.Reason)
	if err != nil {
		s.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}
