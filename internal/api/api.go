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
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
)

//go:embed openapi.yaml
var openAPI []byte

// OpenAPI is the API description served at /api/v1/openapi.yaml.
func OpenAPI() []byte { return openAPI }

// Auth checks bearer tokens and people's passwords, and limits failed attempts per client address and,
// for passwords, per username.
type Auth struct {
	Store *store.Store
	IP    *clientip.Resolver
	Now   func() time.Time

	mu        sync.Mutex
	fails     map[netip.Addr]*window
	userFails map[string]*window
}

type window struct {
	n     int
	start time.Time
}

const (
	maxFails   = 20
	failWindow = 10 * time.Minute
	// maxUserFails is how many wrong passwords one username takes in failWindow, from anywhere, before
	// its sign-ins are refused for the rest of the window.
	maxUserFails = 10
)

// ErrBlocked means too many sign-ins failed recently, from this address or for this username.
var ErrBlocked = errors.New("too many failed sign-ins; try again in a few minutes")

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

// BlockedFor reports how much of the lockout is left, and whether there is one: how long the client
// must wait before trying again.
func (a *Auth) BlockedFor(r *http.Request) (time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.left(a.fails[a.client(r)], maxFails)
}

// Blocked reports whether the client has failed too often recently.
func (a *Auth) Blocked(r *http.Request) bool {
	_, blocked := a.BlockedFor(r)
	return blocked
}

// left is the time until the window that locked out with limit failures ends.
func (a *Auth) left(w *window, limit int) (time.Duration, bool) {
	if w == nil {
		return 0, false
	}
	if left := failWindow - a.now().Sub(w.start); w.n >= limit && left > 0 {
		return left, true
	}
	return 0, false
}

// WaitWords says a lockout's wait as a person reads it: "a minute", "9 minutes". Errors carry it so
// a client knows how long to wait before trying again.
func WaitWords(d time.Duration) string {
	minutes := int((d + time.Minute - 1) / time.Minute)
	if minutes <= 1 {
		return "a minute"
	}
	return strconv.Itoa(minutes) + " minutes"
}

// RetryAfter is a lockout's wait in whole seconds, rounded up, for the Retry-After header.
// The gateway answers its lockouts the same way the API does, so it shares these words.
func RetryAfter(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
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

// Fail counts a failed attempt from the request's client: a wrong code or refresh token on the OAuth
// token endpoint, say.
func (a *Auth) Fail(r *http.Request) { a.fail(r) }

// Client is the request's client address, as the configured proxies give it.
func (a *Auth) Client(r *http.Request) netip.Addr { return a.client(r) }

// Verify checks a token secret for the request, counting failures. It is the machines' check, on the
// gateway: an agent's token carries no scopes of its own there.
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

// Authenticate checks a bearer secret for the API, MCP and the pages, counting failures: a token or an
// OAuth access token, resolved to who it is and what it may do now.
func (a *Auth) Authenticate(r *http.Request, secret string) (store.Principal, bool) {
	if a.Blocked(r) {
		return store.Principal{}, false
	}
	p, ok := a.Store.Authenticate(secret)
	if !ok {
		a.fail(r)
	}
	return p, ok
}

// UserBlockedFor is BlockedFor for one username's wrong passwords, which lock it from anywhere.
func (a *Auth) UserBlockedFor(username string) (time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.left(a.userFails[username], maxUserFails)
}

func (a *Auth) failUser(username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.userFails == nil {
		a.userFails = map[string]*window{}
	}
	w := a.userFails[username]
	if w == nil || a.now().Sub(w.start) >= failWindow {
		w = &window{start: a.now()}
		a.userFails[username] = w
	}
	w.n++
	if len(a.userFails) > 10000 {
		a.userFails = map[string]*window{username: w}
	}
}

// SignIn checks a person's username and password, limited per address and per username like the API's
// tokens: ErrBlocked after too many failures, store.ErrBadCredentials for a wrong username or password.
func (a *Auth) SignIn(r *http.Request, username, password string) (store.Account, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 64 {
		username = username[:64]
	}
	if _, blocked := a.BlockedFor(r); blocked {
		return store.Account{}, ErrBlocked
	}
	if _, blocked := a.UserBlockedFor(username); blocked {
		return store.Account{}, ErrBlocked
	}
	acc, err := a.Store.SignIn(username, password)
	if errors.Is(err, store.ErrBadCredentials) {
		a.fail(r)
		a.failUser(username)
	}
	return acc, err
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
	Settings   *settings.Service // the secrets work's settings (settings.go); nil: none
	Vault      vault             // the secrets vault (vault.go); nil: none
	Secrets    SecretsAPI        // the vault's grants (secrets.go); nil: none
	Status     *status.Service   // the public status pages (status.go); nil: none
}

// Register adds the API's routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openAPI)
	})
	s.registerConfig(mux)
	s.registerSettings(mux)
	s.registerVault(mux)
	s.registerSecrets(mux)
	mux.Handle("GET /api/v1/bridge", s.with("status:read", s.bridgeSummary))
	mux.Handle("GET /api/v1/alerts", s.with("status:read", s.listAlerts))
	mux.Handle("GET /api/v1/alerts/{id}", s.with("status:read", s.getAlert))
	mux.Handle("POST /api/v1/alerts/{id}/ack", s.with("alerts:write", s.ackAlert))
	mux.Handle("GET /api/v1/alerts/{id}/evidence", s.with("status:read", s.evidence))
	mux.Handle("GET /api/v1/alerts/{id}/timeline", s.with("status:read", s.timeline))
	mux.Handle("POST /api/v1/alerts/{id}/notes", s.with("alerts:write", s.addNote))
	mux.Handle("GET /api/v1/changes", s.with("status:read", s.listChanges))
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
	mux.Handle("POST /api/v1/machines/{name}/run", s.withPrincipal("machines:ssh", s.runCommand))
	mux.Handle("GET /api/v1/machines/{name}/file", s.with("machines:ssh", s.readFile))
	mux.Handle("PUT /api/v1/machines/{name}/file", s.with("machines:ssh", s.writeFile))
	mux.Handle("PATCH /api/v1/machines/{name}/file", s.with("machines:ssh", s.editFile))
	mux.Handle("GET /api/v1/sessions", s.with("status:read|machines:ssh", s.listSessions))
	mux.Handle("GET /api/v1/sessions/{id}", s.with("sessions:read|machines:ssh", s.getSession))
	mux.Handle("GET /api/v1/sessions/{id}/recording", s.with("sessions:read|machines:ssh", s.getRecording))
	s.registerToken(mux)
	s.registerPeople(mux)
	mux.Handle("PUT /api/v1/machines/{name}/connector", s.with("machines:write", s.setConnector))
	mux.Handle("POST /api/v1/probe-now", s.with("alerts:write", s.probeNow))
	s.registerStatus(mux)
}

// Routes lists the authenticated routes, for the test that keeps the OpenAPI description in step.
var Routes = []string{
	"GET /api/v1/bridge", "GET /api/v1/alerts", "GET /api/v1/alerts/{id}", "POST /api/v1/alerts/{id}/ack",
	"GET /api/v1/silences", "POST /api/v1/silences", "DELETE /api/v1/silences/{id}", "GET /api/v1/audit",
	"GET /api/v1/ssh", "POST /api/v1/ssh/pause", "POST /api/v1/ssh/resume",
	"GET /api/v1/machines", "POST /api/v1/machines", "DELETE /api/v1/machines/{name}", "POST /api/v1/machines/{name}/run",
	"GET /api/v1/machines/{name}/file", "PUT /api/v1/machines/{name}/file", "PATCH /api/v1/machines/{name}/file",
	"GET /api/v1/sessions", "GET /api/v1/sessions/{id}", "GET /api/v1/sessions/{id}/recording",
	"GET /api/v1/alerts/{id}/evidence", "GET /api/v1/alerts/{id}/timeline", "POST /api/v1/alerts/{id}/notes", "GET /api/v1/changes",
	"PUT /api/v1/machines/{name}/connector",
	"POST /api/v1/probe-now",
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
		if d, blocked := s.Auth.BlockedFor(r); blocked {
			w.Header().Set("Retry-After", RetryAfter(d))
			writeError(w, http.StatusTooManyRequests, "auth.too_many_failures", "too many failed attempts from this address; try again in "+WaitWords(d))
			return
		}
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="beacon"`)
			writeError(w, http.StatusUnauthorized, "auth.required", "a bearer token is required")
			return
		}
		t, ok := s.Auth.Authenticate(r, strings.TrimSpace(secret))
		if !ok {
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
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "total": total})
}
