// Package api is the REST API under /api/v1. Reads and writes need a bearer token with the right
// scope; the OpenAPI description is public. Errors are JSON {"error", "code"} with stable codes.
package api

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/clientip"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
)

//go:embed openapi.yaml
var openAPI []byte

// OpenAPI is the API description served at /api/v1/openapi.yaml.
func OpenAPI() []byte { return openAPI }

// Auth checks bearer tokens and limits failed attempts per client address.
type Auth struct {
	Store *store.Store
	IP    *clientip.Resolver
	Now   func() time.Time

	mu    sync.Mutex
	fails map[netip.Addr]*window
}

type window struct {
	n     int
	start time.Time
}

const (
	maxFails   = 20
	failWindow = 10 * time.Minute
)

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Auth) client(r *http.Request) netip.Addr {
	if a.IP != nil {
		ip, _ := a.IP.ClientIP(r)
		return ip
	}
	return netip.Addr{}
}

// Blocked reports whether the client has failed too often recently.
func (a *Auth) Blocked(r *http.Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.fails[a.client(r)]
	return w != nil && a.now().Sub(w.start) < failWindow && w.n >= maxFails
}

func (a *Auth) fail(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fails == nil {
		a.fails = map[netip.Addr]*window{}
	}
	ip := a.client(r)
	w := a.fails[ip]
	if w == nil || a.now().Sub(w.start) >= failWindow {
		w = &window{start: a.now()}
		a.fails[ip] = w
	}
	w.n++
	if len(a.fails) > 10000 { // forget everyone rather than grow without bound
		a.fails = map[netip.Addr]*window{ip: w}
	}
}

// Verify checks a token secret for the request, counting failures.
func (a *Auth) Verify(r *http.Request, secret string) (store.Token, bool) {
	if a.Blocked(r) {
		return store.Token{}, false
	}
	t, ok := a.Store.VerifyToken(secret)
	if !ok {
		a.fail(r)
	}
	return t, ok
}

// Server serves the API. SSH is nil when the SSH channel is off; SSHAddress is where agents reach it.
// Config is the configuration Beacon holds (its routes are in config.go); nil leaves them out.
type Server struct {
	Bridge     *bridge.Bridge
	SSH        *sshgate.Gate
	SSHAddress string
	Auth       *Auth
	Log        *slog.Logger
	Config     *configdb.Service
}

// Register adds the API's routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openAPI)
	})
	s.registerConfig(mux)
	mux.Handle("GET /api/v1/bridge", s.with("status:read", s.bridgeSummary))
	mux.Handle("GET /api/v1/alerts", s.with("status:read", s.listAlerts))
	mux.Handle("GET /api/v1/alerts/{id}", s.with("status:read", s.getAlert))
	mux.Handle("POST /api/v1/alerts/{id}/ack", s.with("alerts:write", s.ackAlert))
	mux.Handle("GET /api/v1/silences", s.with("status:read", s.listSilences))
	mux.Handle("POST /api/v1/silences", s.with("alerts:write", s.createSilence))
	mux.Handle("DELETE /api/v1/silences/{id}", s.with("alerts:write", s.expireSilence))
	mux.Handle("GET /api/v1/audit", s.with("status:read", s.audit))

	mux.Handle("GET /api/v1/ssh", s.with("status:read", s.sshState))
	mux.Handle("POST /api/v1/ssh/pause", s.with("machines:write", s.sshPause(true)))
	mux.Handle("POST /api/v1/ssh/resume", s.with("machines:write", s.sshPause(false)))
	mux.Handle("GET /api/v1/machines", s.with("status:read|machines:ssh", s.listMachines))
	mux.Handle("POST /api/v1/machines", s.with("machines:write", s.addMachine))
	mux.Handle("DELETE /api/v1/machines/{name}", s.with("machines:write", s.removeMachine))
	mux.Handle("POST /api/v1/machines/{name}/run", s.with("machines:ssh", s.runCommand))
	mux.Handle("GET /api/v1/machines/{name}/file", s.with("machines:ssh", s.readFile))
	mux.Handle("PUT /api/v1/machines/{name}/file", s.with("machines:ssh", s.writeFile))
	mux.Handle("PATCH /api/v1/machines/{name}/file", s.with("machines:ssh", s.editFile))
	mux.Handle("GET /api/v1/sessions", s.with("status:read|machines:ssh", s.listSessions))
	mux.Handle("GET /api/v1/sessions/{id}", s.with("sessions:read|machines:ssh", s.getSession))
	mux.Handle("GET /api/v1/sessions/{id}/recording", s.with("sessions:read|machines:ssh", s.getRecording))
}

// Routes lists the authenticated routes, for the test that keeps the OpenAPI description in step.
var Routes = []string{
	"GET /api/v1/bridge", "GET /api/v1/alerts", "GET /api/v1/alerts/{id}", "POST /api/v1/alerts/{id}/ack",
	"GET /api/v1/silences", "POST /api/v1/silences", "DELETE /api/v1/silences/{id}", "GET /api/v1/audit",
	"GET /api/v1/ssh", "POST /api/v1/ssh/pause", "POST /api/v1/ssh/resume",
	"GET /api/v1/machines", "POST /api/v1/machines", "DELETE /api/v1/machines/{name}", "POST /api/v1/machines/{name}/run",
	"GET /api/v1/machines/{name}/file", "PUT /api/v1/machines/{name}/file", "PATCH /api/v1/machines/{name}/file",
	"GET /api/v1/sessions", "GET /api/v1/sessions/{id}", "GET /api/v1/sessions/{id}/recording",
}

type handler func(w http.ResponseWriter, r *http.Request, who bridge.Actor)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// with guards h with a token carrying scope, or one of the scopes in "a|b".
func (s *Server) with(scope string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Auth.Blocked(r) {
			writeError(w, http.StatusTooManyRequests, "auth.too_many_failures", "too many failed attempts; try again later")
			return
		}
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="linkos-beacon"`)
			writeError(w, http.StatusUnauthorized, "auth.required", "a bearer token is required")
			return
		}
		t, ok := s.Auth.Verify(r, strings.TrimSpace(secret))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="linkos-beacon", error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "auth.invalid", "the token is unknown or has expired")
			return
		}
		if !slices.ContainsFunc(strings.Split(scope, "|"), t.Has) {
			writeError(w, http.StatusForbidden, "auth.scope", "the token lacks the "+strings.ReplaceAll(scope, "|", " or ")+" scope")
			return
		}
		h(w, r, bridge.Actor{Name: t.Name, Via: "api"})
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "bad_input", "send JSON (Content-Type: application/json)")
		return false
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_input", "the body is not valid JSON for this request: "+err.Error())
		return false
	}
	return true
}

// opError maps bridge errors to HTTP answers.
func (s *Server) opError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bridge.ErrNotFound):
		writeError(w, http.StatusNotFound, "alert.not_found", "no alert group with this id")
	case errors.Is(err, bridge.ErrNotFiring):
		writeError(w, http.StatusConflict, "alert.not_firing", "the alert group is not firing")
	case errors.Is(err, bridge.ErrBadInput):
		writeError(w, http.StatusBadRequest, "bad_input", err.Error())
	default:
		s.Log.Error("api operation failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) bridgeSummary(w http.ResponseWriter, _ *http.Request, _ bridge.Actor) {
	writeJSON(w, http.StatusOK, s.Bridge.Summarize())
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	state := r.URL.Query().Get("state")
	if state != "" && state != "firing" && state != "all" {
		writeError(w, http.StatusBadRequest, "bad_input", "state is firing or all")
		return
	}
	alerts := s.Bridge.Groups(state != "all")
	if alerts == nil {
		alerts = []bridge.GroupView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (s *Server) getAlert(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	g, err := s.Bridge.Group(r.PathValue("id"))
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	g, err := s.Bridge.Ack(r.Context(), r.PathValue("id"), who, in.Note)
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) listSilences(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	ss, err := s.Bridge.Silences(r.Context())
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"silences": ss})
}

func (s *Server) createSilence(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	var in struct {
		Alert    string `json:"alert"`
		Duration string `json:"duration"`
		Comment  string `json:"comment"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	d, err := time.ParseDuration(in.Duration)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_input", "duration is like 30m or 4h")
		return
	}
	g, err := s.Bridge.Silence(r.Context(), in.Alert, d, who, in.Comment)
	if err != nil {
		s.opError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) expireSilence(w http.ResponseWriter, r *http.Request, who bridge.Actor) {
	if err := s.Bridge.ExpireSilence(r.Context(), r.PathValue("id"), who); err != nil {
		s.opError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "bad_input", "limit is 1 to 1000")
			return
		}
		limit = n
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.Bridge.Audit(limit)})
}
