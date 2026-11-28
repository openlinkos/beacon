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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
)

//go:embed openapi.yaml
var openAPI []byte

// OpenAPI is the API description served at /api/v1/openapi.yaml.
func OpenAPI() []byte { return openAPI }

// Server serves the API. SSH is nil when the SSH channel is off; SSHAddress is where agents reach it.
// Config is the configuration Beacon holds (its routes are in config.go); nil leaves them out.
type Server struct {
	Bridge                *bridge.Bridge
	SSH                   *sshgate.Gate
	SSHAddress            string
	Auth                  *Auth
	Log                   *slog.Logger
	Config                *configdb.Service
	Status                *status.Service // the public status pages (status.go); nil: none
	Credentials           *secrets.Service
	CredentialsConfigured bool // configured but temporarily unavailable when Credentials is nil
}

// Register adds the API's routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openAPI)
	})
	s.registerCapabilities(mux)
	s.registerConfig(mux)
	s.registerCredentials(mux)
	mux.Handle("GET /api/v1/bridge", s.with("status:read", s.bridgeSummary))
	mux.Handle("GET /api/v1/alerts", s.with("status:read", s.listAlerts))
	mux.Handle("GET /api/v1/alerts/{id}", s.with("status:read", s.getAlert))
	mux.Handle("POST /api/v1/alerts/{id}/ack", s.with("alerts:write", s.ackAlert))
	mux.Handle("GET /api/v1/alerts/{id}/evidence", s.withPrincipal("status:read", s.evidence))
	mux.Handle("GET /api/v1/alerts/{id}/timeline", s.withPrincipal("status:read", s.timeline))
	mux.Handle("GET /api/v1/incident-outcomes", s.withPrincipal("status:read", s.incidentOutcomes))
	mux.Handle("GET /api/v1/incident-outcomes/{id}", s.withPrincipal("status:read", s.incidentOutcome))
	mux.Handle("GET /api/v1/incident-episodes", s.with("status:read", s.incidentEpisodes))
	mux.Handle("POST /api/v1/alerts/{id}/notes", s.with("alerts:write", s.addNote))
	mux.Handle("PUT /api/v1/alerts/{id}/agent-runs/{delivery}", s.with(bridge.AgentRunScope, s.reportAgentRun))
	mux.Handle("GET /api/v1/agent-hooks/{name}/first-drill", s.with(bridge.AgentRunScope, s.checkDrillHook))
	mux.Handle("GET /api/v1/changes", s.with("status:read", s.listChanges))
	mux.Handle("GET /api/v1/silences", s.with("status:read", s.listSilences))
	mux.Handle("POST /api/v1/silences", s.with("alerts:write", s.createSilence))
	mux.Handle("DELETE /api/v1/silences/{id}", s.with("alerts:write", s.expireSilence))
	mux.Handle("GET /api/v1/audit", s.withPrincipal("status:read", s.audit))

	mux.Handle("GET /api/v1/ssh", s.with("status:read|machines:ssh", s.sshState))
	mux.Handle("POST /api/v1/ssh/pause", s.with("machines:write", s.sshPause(true)))
	mux.Handle("POST /api/v1/ssh/resume", s.with("machines:write", s.sshPause(false)))
	mux.Handle("GET /api/v1/machines", s.with("status:read|machines:ssh", s.listMachines))
	mux.Handle("POST /api/v1/machines", s.with("machines:write", s.addMachine))
	mux.Handle("PATCH /api/v1/machines/{name}", s.with("machines:write", s.updateMachine))
	mux.Handle("GET /api/v1/machines/{name}/history", s.with("status:read|machines:ssh", s.machineHistory))
	mux.Handle("POST /api/v1/machines/{name}/restore", s.with("machines:write", s.restoreMachine))
	mux.Handle("DELETE /api/v1/machines/{name}", s.with("machines:write", s.removeMachine))
	mux.Handle("POST /api/v1/machines/{name}/run", s.with("machines:ssh", s.runCommand))
	mux.Handle("POST /api/v1/machines/{name}/check", s.with("machines:ssh", s.checkMachineSSH))
	mux.Handle("POST /api/v1/machines/{name}/search", s.with("machines:ssh", s.searchFiles))
	mux.Handle("GET /api/v1/machines/{name}/file", s.with("machines:ssh", s.readFile))
	mux.Handle("PUT /api/v1/machines/{name}/file", s.with("machines:ssh", s.writeFile))
	mux.Handle("PATCH /api/v1/machines/{name}/file", s.with("machines:ssh", s.editFile))
	mux.Handle("GET /api/v1/sessions", s.withPrincipal("status:read|machines:ssh", s.listSessions))
	mux.Handle("GET /api/v1/sessions/{id}", s.withPrincipal("sessions:read|machines:ssh", s.getSession))
	mux.Handle("GET /api/v1/sessions/{id}/recording", s.withPrincipal("sessions:read|machines:ssh", s.getRecording))
	s.registerToken(mux)
	s.registerPeople(mux)
	s.registerAgents(mux)
	s.registerShareLinks(mux)
	mux.Handle("PUT /api/v1/machines/{name}/connector", s.with("machines:write", s.setConnector))
	mux.Handle("POST /api/v1/probe-now", s.with("alerts:write", s.probeNow))
	s.registerStatus(mux)
}

// Routes lists the authenticated routes, for the test that keeps the OpenAPI description in step.
var Routes = []string{
	"GET /api/v1/bridge", "GET /api/v1/alerts", "GET /api/v1/alerts/{id}", "POST /api/v1/alerts/{id}/ack",
	"GET /api/v1/silences", "POST /api/v1/silences", "DELETE /api/v1/silences/{id}", "GET /api/v1/audit",
	"GET /api/v1/ssh", "POST /api/v1/ssh/pause", "POST /api/v1/ssh/resume",
	"GET /api/v1/machines", "POST /api/v1/machines", "PATCH /api/v1/machines/{name}", "DELETE /api/v1/machines/{name}", "POST /api/v1/machines/{name}/run",
	"GET /api/v1/machines/{name}/history", "POST /api/v1/machines/{name}/restore",
	"POST /api/v1/machines/{name}/check", "POST /api/v1/machines/{name}/search", "GET /api/v1/machines/{name}/file", "PUT /api/v1/machines/{name}/file", "PATCH /api/v1/machines/{name}/file",
	"GET /api/v1/sessions", "GET /api/v1/sessions/{id}", "GET /api/v1/sessions/{id}/recording",
	"GET /api/v1/alerts/{id}/evidence", "GET /api/v1/alerts/{id}/timeline", "POST /api/v1/alerts/{id}/notes", "GET /api/v1/changes",
	"PUT /api/v1/alerts/{id}/agent-runs/{delivery}", "GET /api/v1/agent-hooks/{name}/first-drill",
	"GET /api/v1/incident-outcomes", "GET /api/v1/incident-outcomes/{id}",
	"GET /api/v1/incident-episodes",
	"PUT /api/v1/machines/{name}/connector",
	"POST /api/v1/probe-now",
	"POST /api/v1/share-links/{kind}/{id}", "POST /api/v1/share-links/{kind}/{id}/revoke",
	"GET /api/v1/team-credentials", "POST /api/v1/team-credentials/{id}/use",
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

// DocsURL is the API's reference, where an answer to a request without a valid token points: whoever
// sends one may not know Beacon yet, and that page says how to get a token and what it may do.
const DocsURL = "https://beacon.openlinkos.com/reference/api"

// writeError answers with an error and its stable code; a 401 also carries docs, the reference's address.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	body := map[string]string{"error": msg, "code": code}
	if status == http.StatusUnauthorized {
		body["docs"] = DocsURL
	}
	writeJSON(w, status, body)
}

// with guards h with a credential (a token or an OAuth access token) that may use scope now, or one of
// the scopes in "a|b"; h is given who it is, as the audit log names it.
func (s *Server) with(scope string, h handler) http.Handler {
	return s.withPrincipal(scope, func(w http.ResponseWriter, r *http.Request, p store.Principal) {
		h(w, r, bridge.ActorOf(p, "api"))
	})
}

// withPrincipal is with for a handler that needs the principal itself (token.go); an empty scope admits
// any credential.
func (s *Server) withPrincipal(scope string, h func(w http.ResponseWriter, r *http.Request, p store.Principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lockedOut := func(d time.Duration) {
			w.Header().Set("Retry-After", RetryAfter(d))
			writeError(w, http.StatusTooManyRequests, "auth.too_many_failures", "too many failed attempts from this address; try again in "+WaitWords(d))
		}
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			if d, blocked := s.Auth.BlockedFor(r); blocked {
				lockedOut(d)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="beacon"`)
			writeError(w, http.StatusUnauthorized, "auth.required", "a bearer token is required")
			return
		}
		t, d, err := s.Auth.Check(r, strings.TrimSpace(secret))
		if errors.Is(err, ErrBlocked) {
			lockedOut(d)
			return
		}
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="beacon", error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "auth.invalid", "the token is unknown or has expired")
			return
		}
		if scope != "" && !slices.ContainsFunc(strings.Split(scope, "|"), t.Has) {
			writeError(w, http.StatusForbidden, "auth.scope", ScopeProblem(t, scope))
			return
		}
		h(w, r, t)
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

func (s *Server) audit(w http.ResponseWriter, r *http.Request, p store.Principal) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "bad_input", "limit is 1 to 1000")
			return
		}
		limit = n
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_input", "offset is 0 or more")
			return
		}
		offset = n
	}
	f := store.AuditFilter{Limit: limit, Offset: offset,
		Actor:  r.URL.Query().Get("actor"),
		Action: r.URL.Query().Get("action"),
		Target: r.URL.Query().Get("target")}
	for name, to := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := r.URL.Query().Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_input", name+" is an RFC 3339 time")
				return
			}
			*to = t
		}
	}
	entries, total, err := s.Bridge.AuditQuery(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit.unavailable", "the audit log cannot be read")
		return
	}
	s.Bridge.FilterSessionAudit(p, entries)
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "total": total})
}
