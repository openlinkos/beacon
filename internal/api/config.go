package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
)

// The configuration's routes: the catalogue (locations, heartbeats, resources), the target sets and
// the modules, each read and changed in the shape of its file, and the history of changes with revert.

type configKind struct{ kind, list, param string }

var configKinds = []configKind{
	{configdb.KindLocation, "locations", "id"},
	{configdb.KindHeartbeat, "heartbeats", "name"},
	{configdb.KindResource, "resources", "name"},
	{configdb.KindTargets, "targets", "name"},
	{configdb.KindModule, "modules", "name"},
}

func configRoutes() []string {
	out := []string{"GET /api/v1/config"}
	for _, k := range configKinds {
		base, item := "/api/v1/config/"+k.list, "/api/v1/config/"+k.list+"/{"+k.param+"}"
		out = append(out, "GET "+base, "GET "+item, "PUT "+item, "DELETE "+item)
	}
	return append(out, "GET /api/v1/config/history", "GET /api/v1/config/history/{version}", "POST /api/v1/config/history/{version}/revert")
}

func init() { Routes = append(Routes, configRoutes()...) }

// configRead is who may read the configuration: anyone who may read the status, and whoever may change it.
const configRead = "status:read|config:write"

func (s *Server) registerConfig(mux *http.ServeMux) {
	if s.Config == nil {
		return
	}
	mux.Handle("GET /api/v1/config", s.with(configRead, s.getConfig(configKind{})))
	for _, k := range configKinds {
		base, item := "/api/v1/config/"+k.list, "/api/v1/config/"+k.list+"/{"+k.param+"}"
		mux.Handle("GET "+base, s.with(configRead, s.getConfig(k)))
		mux.Handle("GET "+item, s.with(configRead, s.getConfigItem(k)))
		mux.Handle("PUT "+item, s.with("config:write", s.putConfigItem(k)))
		mux.Handle("DELETE "+item, s.with("config:write", s.deleteConfigItem(k)))
	}
	mux.Handle("GET /api/v1/config/history", s.with(configRead, s.configHistory))
	mux.Handle("GET /api/v1/config/history/{version}", s.with(configRead, s.configChange))
	mux.Handle("POST /api/v1/config/history/{version}/revert", s.with("config:write", s.revertConfig))
}

func configActor(who bridge.Actor) configdb.Actor {
	return configdb.Actor{Name: who.Name, Via: who.Via}
}

// configError maps the configuration's errors to HTTP answers.
func (s *Server) configError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, configdb.ErrNotFound):
		writeError(w, http.StatusNotFound, "config.not_found", err.Error())
	case errors.Is(err, configdb.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "config.invalid", err.Error())
	case errors.Is(err, configdb.ErrConflict):
		writeError(w, http.StatusConflict, "config.conflict", err.Error())
	case errors.Is(err, configdb.ErrBadInput):
		writeError(w, http.StatusBadRequest, "bad_input", err.Error())
	default:
		s.Log.Error("configuration change failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) getConfig(k configKind) handler {
	return func(w http.ResponseWriter, _ *http.Request, _ bridge.Actor) {
		v, err := s.Config.Get(k.kind, "")
		if err != nil {
			s.configError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) getConfigItem(k configKind) handler {
	return func(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
		v, err := s.Config.Item(k.kind, r.PathValue(k.param))
		if err != nil {
			s.configError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) putConfigItem(k configKind) handler {
	return func(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
		var v json.RawMessage
		if !readBody(w, r, &v, 1<<20) {
			return
		}
		ifVersion, ok := ifVersion(w, r)
		if !ok {
			return
		}
		ch, err := s.Config.PutIf(r.Context(), configActor(who), k.kind, r.PathValue(k.param), v, r.URL.Query().Get("note"), ifVersion)
		if err != nil {
			s.configError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

func (s *Server) deleteConfigItem(k configKind) handler {
	return func(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
		ifVersion, ok := ifVersion(w, r)
		if !ok {
			return
		}
		ch, err := s.Config.DeleteIf(r.Context(), configActor(who), k.kind, r.PathValue(k.param), r.URL.Query().Get("note"), ifVersion)
		if err != nil {
			s.configError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

// ifVersion reads the query's if_version, the version the caller's change is based on (0 when absent),
// answering 400 when it is not one.
func ifVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	s := r.URL.Query().Get("if_version")
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 1 {
		writeError(w, http.StatusBadRequest, "bad_input", "if_version is the version you last saw, a whole number from 1")
		return 0, false
	}
	return v, true
}

// version reads the path's version, answering 400 when it is not one.
func version(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || v < 1 {
		writeError(w, http.StatusBadRequest, "bad_input", "a version is a whole number from 1")
		return 0, false
	}
	return v, true
}

func (s *Server) configHistory(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	q := r.URL.Query()
	f := configdb.HistoryFilter{Kind: q.Get("kind"), Key: q.Get("key")}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "bad_input", "before is a version, a whole number from 1")
			return
		}
		f.Before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "bad_input", "limit is 1 to 1000")
			return
		}
		f.Limit = n
	}
	list, err := s.Config.History(f)
	if err != nil {
		s.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": list})
}

func (s *Server) configChange(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	v, ok := version(w, r)
	if !ok {
		return
	}
	ch, err := s.Config.Change(v)
	if err != nil {
		s.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) revertConfig(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	v, ok := version(w, r)
	if !ok {
		return
	}
	ch, err := s.Config.Revert(r.Context(), configActor(who), v, r.URL.Query().Get("note"))
	if err != nil {
		s.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}
