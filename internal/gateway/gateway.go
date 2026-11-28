// Package gateway is the one door for machines (metrics.<domain>): probes and hosts write their samples
// (Prometheus remote write) and fetch the targets they check, job hosts push heartbeats, agents query
// metrics, the rule engine and Flux post alerts (Alertmanager's API v2, taken by the router), and CI and
// Flux post change events (deployments, reconciliations). It replaces a separate auth proxy: every request carries a token from
// this service's store with the scope for what it does, so the tokens are created, listed, revoked and
// audited in one place.
//
// Probe and host tokens name the location or host they speak for. The gateway, not the machine, puts
// that name (and the location's region and network, from the catalogue) on every sample, and whatever
// labels or parameters the machine sends cannot change it. A location whose machine has expired (see
// catalog.Expected) cannot write any more.
package gateway

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/exercise"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// Deps are what the gateway needs. Alerts takes posted alerts (the router), with the name of the
// alerts:send token that posted them, or "" for the rule engine's; RulesToken is the bearer the rule
// engine sends, accepted for posting alerts besides tokens with the alerts:send scope. Auth checks the
// tokens; the gateway counts failed attempts on its own (api.Auth.Separate), apart from the API's.
type Deps struct {
	Auth           *api.Auth
	Catalog        func() *catalog.Catalog
	Targets        func() *targets.Set
	Modules        func() []byte // the probe module file, handed to probes as is
	VM             *url.URL
	Alerts         func(sender string, in []router.Postable) error
	Changes        ChangeSink // takes change events (changes.go); nil refuses them
	RulesToken     string
	Metrics        *metrics.Metrics
	Log            *slog.Logger
	Now            func() time.Time
	Requests       *probenow.Board     // probing on demand (requests.go); nil answers 404 as an older gateway
	Exercise       *exercise.Authority // nil leaves drill observation disabled
	ExerciseTarget string
	// Leader, when set, is whether this instance holds the HA lease. Writes are refused on a
	// standby; reads of targets and modules still answer.
	Leader func() bool
}

// MaxBody is the largest request body accepted (a remote-write batch is compressed and far smaller).
const MaxBody = 32 << 20

// readPaths are the VictoriaMetrics query endpoints open to metrics:read.
var readPaths = []string{
	"GET /api/v1/query", "POST /api/v1/query", "GET /api/v1/query_range", "POST /api/v1/query_range",
	"GET /api/v1/series", "POST /api/v1/series", "GET /api/v1/labels", "GET /api/v1/label/{name}/values",
	"GET /api/v1/metadata", "GET /api/v1/status/{what}", "GET /api/v1/export", "GET /api/v1/rules", "GET /api/v1/alerts",
}

type gateway struct {
	Deps
	vm        *httputil.ReverseProxy
	rulesHash [sha256.Size]byte // of RulesToken, compared in constant time

	mu        sync.Mutex
	lastWrite map[string]time.Time // by location or host

	changeRate limiter // change events per token (limit.go)
	alertRate  limiter // alert posts per alerts:send token (limit.go)
}

// Handler builds the gateway's router.
func Handler(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Auth != nil {
		d.Auth = d.Auth.Separate()
	}
	g := &gateway{Deps: d, lastWrite: map[string]time.Time{}}
	g.rulesHash = sha256.Sum256([]byte(d.RulesToken))
	g.vm = proxy(d.VM)
	if d.Metrics != nil {
		d.Metrics.Register("beacon_gateway_last_write_timestamp_seconds", "When a probe location or host last wrote through the gateway", "gauge", g.lastWrites)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/write", g.with("write", []string{"probe:write", "host:write"}, g.write))
	mux.Handle("GET /api/v1/targets", g.with("targets", []string{"probe:write"}, g.targets))
	mux.Handle("GET /api/v1/modules", g.with("modules", []string{"probe:write"}, g.modules))
	mux.Handle("GET /api/v1/agent", g.with("agent", []string{"probe:write"}, g.agent))
	mux.Handle("POST /api/v1/import/prometheus", g.with("import", []string{"probe:write", "host:write", "heartbeat:write"}, g.importText))
	for _, p := range readPaths {
		mux.Handle(p, g.with("read", []string{"metrics:read"}, g.read(p)))
	}
	mux.HandleFunc("POST /api/v2/alerts", g.rulesAlerts)
	mux.Handle("POST /api/v1/changes", g.with("changes", []string{"changes:write"}, g.change))
	mux.Handle("POST /api/v1/changes/flux", g.with("changes", []string{"changes:write"}, g.fluxChange))
	mux.Handle("GET /api/v1/agent/requests", g.with("requests", []string{"probe:write"}, g.requests))
	mux.Handle("POST /api/v1/agent/requests/{id}", g.with("answers", []string{"probe:write"}, g.answer))
	if d.Exercise != nil {
		mux.Handle("POST "+exercise.ObservationPath, g.with("exercise_observations", []string{"probe:write"}, g.observeExercise))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "no such endpoint on the metrics gateway")
	})
	return mux
}

// proxy forwards to base, keeping the path and the query that the handler set, and never the token. The
// path is the handler's alone: not the request's own spelling of it (RawPath).
func proxy(base *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host = base.Scheme, base.Host
			r.Out.URL.Path = strings.TrimSuffix(base.Path, "/") + r.In.URL.Path
			r.Out.URL.RawPath = ""
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Host = base.Host
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("Cookie")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			var refused *importError // the import's own text broke its limits on the way (imports.go)
			if errors.As(err, &refused) {
				fail(w, http.StatusBadRequest, refused.code, refused.msg)
				return
			}
			fail(w, http.StatusBadGateway, "upstream", "the backend did not answer")
		},
	}
}

type handler func(w http.ResponseWriter, r *http.Request, t store.Token)

// refuseStandby answers 503 when this instance does not hold the leader lease.
func (g *gateway) refuseStandby(w http.ResponseWriter) bool {
	if g.Leader == nil || g.Leader() {
		return false
	}
	fail(w, http.StatusServiceUnavailable, "standby", "this instance does not hold the leader lease")
	return true
}

// counted wraps w to record the status; done counts the request by route and status.
func (g *gateway) counted(route string, w http.ResponseWriter) (rec *status, done func()) {
	rec = &status{ResponseWriter: w, code: http.StatusOK}
	return rec, func() {
		if g.Metrics != nil {
			g.Metrics.Inc("beacon_gateway_requests_total", "Requests to the metrics gateway by route and status",
				metrics.Labels("route", route, "code", strconv.Itoa(rec.code)))
		}
	}
}

// with checks the token for one of scopes, then counts the request by route and status.
func (g *gateway) with(route string, scopes []string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, done := g.counted(route, w)
		defer done()
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			fail(rec, http.StatusUnauthorized, "auth.required", "send a bearer token")
			return
		}
		t, wait, err := g.Auth.CheckToken(r, secret)
		if !admitted(rec, wait, err) {
			return
		}
		serve(rec, r, t, scopes, h)
	})
}

// admitted answers a credential the lockout or the check refused: 429 with the wait, or 401.
func admitted(w http.ResponseWriter, wait time.Duration, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, api.ErrBlocked):
		w.Header().Set("Retry-After", api.RetryAfter(wait))
		fail(w, http.StatusTooManyRequests, "auth.too_many_failures", "too many failed attempts from this address; try again in "+api.WaitWords(wait))
	default:
		fail(w, http.StatusUnauthorized, "auth.invalid", "the token is unknown or expired")
	}
	return false
}

// serve hands the request to h when the token has one of scopes.
func serve(w http.ResponseWriter, r *http.Request, t store.Token, scopes []string, h handler) {
	for _, sc := range scopes {
		if t.Has(sc) {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
			h(w, r, t)
			return
		}
	}
	fail(w, http.StatusForbidden, "auth.scope", "the token lacks the scope "+strings.Join(scopes, " or "))
}

// labels are the labels the gateway puts on what the token writes, or a reason it may not write. job
// is the kind of token, probe or host (heartbeat in importText), whatever the machine says, so that
// the rules can tell a probe's results from what anything else writes.
func (g *gateway) labels(t store.Token) ([][2]string, string) {
	if t.Has("probe:write") {
		cat := g.Catalog()
		l, ok := cat.Location(t.Subject)
		switch {
		case !ok:
			return nil, "location " + t.Subject + " is not in the catalogue"
		case !cat.Expected(l, g.Now()):
			return nil, "the machine of location " + t.Subject + " has expired"
		}
		return [][2]string{{"location", l.ID}, {"region", l.Region}, {"network", l.Network}, {"host", l.ID}, {"job", "probe"}}, ""
	}
	return [][2]string{{"host", t.Subject}, {"job", "host"}}, ""
}

func (g *gateway) write(w http.ResponseWriter, r *http.Request, t store.Token) {
	if g.refuseStandby(w) {
		return
	}
	labels, why := g.labels(t)
	if why != "" {
		fail(w, http.StatusForbidden, "location.inactive", why)
		return
	}
	q := url.Values{}
	for _, l := range labels {
		q.Add("extra_label", l[0]+"="+l[1])
	}
	r.URL.Path, r.URL.RawQuery = "/api/v1/write", q.Encode()
	g.mu.Lock()
	g.lastWrite[t.Subject] = g.Now()
	g.mu.Unlock()
	g.vm.ServeHTTP(w, r)
}

// active is the token's location when it is in the catalogue and its machine has not expired; otherwise
// it answers 403 and reports false.
func (g *gateway) active(w http.ResponseWriter, t store.Token) (*catalog.Catalog, catalog.Location, bool) {
	cat := g.Catalog()
	l, ok := cat.Location(t.Subject)
	if !ok || !cat.Expected(l, g.Now()) {
		fail(w, http.StatusForbidden, "location.inactive", "location "+t.Subject+" is not active")
		return nil, catalog.Location{}, false
	}
	return cat, l, true
}

// agent tells a probe agent its settings from the catalogue.
func (g *gateway) agent(w http.ResponseWriter, _ *http.Request, t store.Token) {
	cat, l, ok := g.active(w, t)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	settings := targets.Settings{Location: l.ID, ProbeTimeoutSeconds: l.Probe.Timeout.D().Seconds(), ProbeRetries: l.Probe.Retries}
	if g.Exercise != nil {
		now := g.Now()
		run := g.Auth.Store.DrillRun()
		if run.ID != "" && run.Active(now) {
			settings.Exercise = g.Exercise.Issue(run, g.exerciseAssignment(cat, l), l.ID, t.Hash, now)
		}
	}
	_ = json.NewEncoder(w).Encode(settings)
}

func (g *gateway) targets(w http.ResponseWriter, _ *http.Request, t store.Token) {
	cat, l, ok := g.active(w, t)
	if !ok {
		return
	}
	ids := make([]string, 0, len(cat.Locations))
	for _, x := range cat.Locations {
		ids = append(ids, x.ID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(g.Targets().For(l.ID, l.Region, ids))
}

// importText takes samples in the Prometheus text format (what beacon-probe pushes, and heartbeats),
// labelled like remote write: a probe's with its location, a host's with its name. A heartbeat token
// writes only its heartbeat's sample, labelled with the heartbeat it was enrolled for. The text is
// bounded on its way (imports.go).
func (g *gateway) importText(w http.ResponseWriter, r *http.Request, t store.Token) {
	if g.refuseStandby(w) {
		return
	}
	q := url.Values{}
	if t.Has("probe:write") || t.Has("host:write") {
		labels, why := g.labels(t)
		if why != "" {
			fail(w, http.StatusForbidden, "location.inactive", why)
			return
		}
		for _, l := range labels {
			q.Add("extra_label", l[0]+"="+l[1])
		}
		body, err := importBody(r, importLimits{bytes: maxImportBytes, samples: maxImportSamples})
		if refused(w, err) {
			return
		}
		r.Body = io.NopCloser(body)
		g.mu.Lock()
		g.lastWrite[t.Subject] = g.Now()
		g.mu.Unlock()
	} else {
		if t.Subject == "" {
			fail(w, http.StatusForbidden, "heartbeat.unnamed", "this heartbeat token names no heartbeat; enrol the heartbeat with beacon enroll heartbeat NAME and send with its token")
			return
		}
		b, err := readImport(r, importLimits{bytes: maxHeartbeatBytes, samples: maxHeartbeatSamples, only: heartbeatMetric})
		if refused(w, err) {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		for _, l := range [][2]string{{"job", "heartbeat"}, {"heartbeat", t.Subject}, {"source", "heartbeat"}, {"sender", t.Subject}} {
			q.Add("extra_label", l[0]+"="+l[1])
		}
	}
	r.ContentLength = -1 // decompressed, of a length not known in advance
	r.Header.Del("Content-Length")
	r.Header.Del("Content-Encoding")
	r.URL.Path, r.URL.RawQuery = "/api/v1/import/prometheus", q.Encode()
	g.vm.ServeHTTP(w, r)
}

// refused answers an import's error, if there is one, and reports whether it did.
func refused(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var ie *importError
	if errors.As(err, &ie) {
		fail(w, http.StatusBadRequest, ie.code, ie.msg)
	} else {
		fail(w, http.StatusBadRequest, "bad_input", "the body could not be read: "+err.Error())
	}
	return true
}

func (g *gateway) modules(w http.ResponseWriter, _ *http.Request, _ store.Token) {
	if g.Modules == nil {
		fail(w, http.StatusNotFound, "not_found", "no module file here")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(g.Modules())
}

// readName is what a read route's wildcard takes: a label name, or a status page's name. Nothing that
// could spell out a path of its own: no slashes, backslashes, percent signs or dot segments.
var readName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,255}$`)

// read passes a query to VictoriaMetrics on the path of the route that matched (pattern), rebuilt from
// it, so that a request reaches no other route there whatever its path spells out.
func (g *gateway) read(pattern string) handler {
	_, path, _ := strings.Cut(pattern, " ")
	return func(w http.ResponseWriter, r *http.Request, _ store.Token) {
		out := path
		for _, name := range []string{"name", "what"} {
			wildcard := "{" + name + "}"
			if !strings.Contains(out, wildcard) {
				continue
			}
			v := r.PathValue(name)
			if !readName.MatchString(v) {
				fail(w, http.StatusBadRequest, "bad_input", "a "+name+" in the path is a plain name: letters, digits, underscores, dots, colons and dashes")
				return
			}
			out = strings.Replace(out, wildcard, v, 1)
		}
		r.URL.Path, r.URL.RawPath = out, ""
		g.vm.ServeHTTP(w, r)
	}
}

// rulesAlerts takes alerts from the rule engine (its own bearer token) or from anyone with alerts:send.
// The rule engine's token is checked under the same lockout as every token, and a wrong one counts.
func (g *gateway) rulesAlerts(w http.ResponseWriter, r *http.Request) {
	rec, done := g.counted("alerts", w)
	defer done()
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		fail(rec, http.StatusUnauthorized, "auth.required", "send a bearer token")
		return
	}
	rules := false
	var t store.Token
	wait, err := g.Auth.Attempt(r, secret, func() (bool, bool) {
		if rules = g.isRulesToken(secret); rules {
			return true, false
		}
		var ok bool
		t, ok = g.Auth.Store.VerifyToken(secret)
		return ok, !ok && g.Auth.Store.Issued(secret)
	})
	if !admitted(rec, wait, err) {
		return
	}
	if rules {
		g.ingestAlerts(rec, r, g.Exercise != nil)
		return
	}
	serve(rec, r, t, []string{"alerts:send"}, g.alerts)
}

// isRulesToken compares secret with the rule engine's token in constant time, through their hashes so
// that not even the length shows.
func (g *gateway) isRulesToken(secret string) bool {
	h := sha256.Sum256([]byte(secret))
	return g.RulesToken != "" && subtle.ConstantTimeCompare(h[:], g.rulesHash[:]) == 1
}

// alerts takes alerts from a token with alerts:send. They are the token's own: the router labels them
// with its name (router.SenderLabel) and keeps them apart from the rule engine's; the Watchdog and alerts
// of severity none, which only the rule engine sends, are dropped and counted; and the token posts at
// most alertLimit times a minute (limit.go).
func (g *gateway) alerts(w http.ResponseWriter, r *http.Request, t store.Token) {
	if g.alertsLimited(w, t) {
		return
	}
	g.ingest(w, r, t.Name, false)
}

// ingestAlerts takes the rule engine's alerts.
func (g *gateway) ingestAlerts(w http.ResponseWriter, r *http.Request, trustedExerciseRule bool) {
	g.ingest(w, r, "", trustedExerciseRule)
}

// ingest takes the alerts sender posted ("" for the rule engine).
func (g *gateway) ingest(w http.ResponseWriter, r *http.Request, sender string, trustedExerciseRule bool) {
	if g.refuseStandby(w) {
		return
	}
	if g.Alerts == nil {
		fail(w, http.StatusServiceUnavailable, "upstream", "alerts are not taken here")
		return
	}
	var in []router.Postable
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad_input", "the body must be a JSON array of alerts")
		return
	}
	if !trustedExerciseRule {
		for _, alert := range in {
			delete(alert.Annotations, exercise.ActiveAtAnnotation)
		}
	}
	if sender != "" {
		kept := in[:0]
		for _, alert := range in {
			if !router.Reserved(alert.Labels) {
				kept = append(kept, alert)
			} else if g.Metrics != nil {
				g.Metrics.Inc("beacon_gateway_alerts_dropped_total", "Alerts dropped because only the rule engine sends the Watchdog and alerts of severity none",
					metrics.Labels("token", sender))
			}
		}
		in = kept
	}
	if err := g.Alerts(sender, in); err != nil {
		fail(w, http.StatusBadRequest, "bad_input", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (g *gateway) lastWrites() []metrics.Sample {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]metrics.Sample, 0, len(g.lastWrite))
	for who, at := range g.lastWrite {
		out = append(out, metrics.Sample{Labels: metrics.Labels("subject", who), Value: float64(at.Unix())})
	}
	return out
}

type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *status) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// fail answers with an error and its stable code; a 401 also carries docs, as the API's do.
func fail(w http.ResponseWriter, code int, errCode, msg string) {
	body := map[string]string{"error": msg, "code": errCode}
	if code == http.StatusUnauthorized {
		body["docs"] = api.DocsURL
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
