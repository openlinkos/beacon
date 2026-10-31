// Package gateway is the one door for machines (metrics.<domain>): probes and hosts write their samples
// (Prometheus remote write) and fetch the targets they check, job hosts push heartbeats, agents query
// metrics, and the rule engine and Flux post alerts (Alertmanager's API v2, taken by the router). It replaces a separate auth proxy: every request carries a token from
// this service's store with the scope for what it does, so the tokens are created, listed, revoked and
// audited in one place.
//
// Probe and host tokens name the location or host they speak for. The gateway, not the machine, puts
// that name (and the location's region and network, from the catalogue) on every sample, and whatever
// labels or parameters the machine sends cannot change it. A location whose machine has expired (see
// catalog.Expected) cannot write any more.
package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// Deps are what the gateway needs. Alerts takes posted alerts (the router); RulesToken is the bearer the
// rule engine sends, accepted for posting alerts besides tokens with the alerts:send scope.
type Deps struct {
	Auth       *api.Auth
	Catalog    func() *catalog.Catalog
	Targets    func() *targets.Set
	Modules    func() []byte // the probe module file, handed to probes as is
	VM         *url.URL
	Alerts     func([]router.Postable) error
	RulesToken string
	Metrics    *metrics.Metrics
	Log        *slog.Logger
	Now        func() time.Time
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
	vm *httputil.ReverseProxy

	mu        sync.Mutex
	lastWrite map[string]time.Time // by location or host
}

// Handler builds the gateway's router.
func Handler(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	g := &gateway{Deps: d, lastWrite: map[string]time.Time{}}
	g.vm = proxy(d.VM)
	if d.Metrics != nil {
		d.Metrics.Register("linkos_gateway_last_write_timestamp_seconds", "When a probe location or host last wrote through the gateway", "gauge", g.lastWrites)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/write", g.with("write", []string{"probe:write", "host:write"}, g.write))
	mux.Handle("GET /api/v1/targets", g.with("targets", []string{"probe:write"}, g.targets))
	mux.Handle("GET /api/v1/modules", g.with("modules", []string{"probe:write"}, g.modules))
	mux.Handle("POST /api/v1/import/prometheus", g.with("import", []string{"probe:write", "host:write", "heartbeat:write"}, g.importText))
	for _, p := range readPaths {
		mux.Handle(p, g.with("read", []string{"metrics:read"}, g.read))
	}
	mux.HandleFunc("POST /api/v2/alerts", g.rulesAlerts)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "no such endpoint on the metrics gateway")
	})
	return mux
}

// proxy forwards to base, keeping the path and the query that the handler set, and never the token.
func proxy(base *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host = base.Scheme, base.Host
			r.Out.URL.Path = strings.TrimSuffix(base.Path, "/") + r.In.URL.Path
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Host = base.Host
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("Cookie")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			fail(w, http.StatusBadGateway, "upstream", "the backend did not answer")
		},
	}
}

type handler func(w http.ResponseWriter, r *http.Request, t store.Token)

// with checks the token for one of scopes, then counts the request by route and status.
func (g *gateway) with(route string, scopes []string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &status{ResponseWriter: w, code: http.StatusOK}
		defer func() {
			if g.Metrics != nil {
				g.Metrics.Inc("linkos_gateway_requests_total", "Requests to the metrics gateway by route and status",
					metrics.Labels("route", route, "code", strconv.Itoa(rec.code)))
			}
		}()
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			fail(rec, http.StatusUnauthorized, "auth.required", "send a bearer token")
			return
		}
		if g.Auth.Blocked(r) {
			fail(rec, http.StatusTooManyRequests, "auth.too_many_failures", "too many failed attempts; try again later")
			return
		}
		t, ok := g.Auth.Verify(r, secret)
		if !ok {
			fail(rec, http.StatusUnauthorized, "auth.invalid", "the token is unknown or expired")
			return
		}
		for _, sc := range scopes {
			if t.Has(sc) {
				r.Body = http.MaxBytesReader(rec, r.Body, MaxBody)
				h(rec, r, t)
				return
			}
		}
		fail(rec, http.StatusForbidden, "auth.scope", "the token lacks the scope "+strings.Join(scopes, " or "))
	})
}

// labels are the labels the gateway puts on what the token writes, or a reason it may not write.
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
		return [][2]string{{"location", l.ID}, {"region", l.Region}, {"network", l.Network}, {"host", l.ID}}, ""
	}
	return [][2]string{{"host", t.Subject}}, ""
}

func (g *gateway) write(w http.ResponseWriter, r *http.Request, t store.Token) {
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

func (g *gateway) targets(w http.ResponseWriter, _ *http.Request, t store.Token) {
	cat := g.Catalog()
	l, ok := cat.Location(t.Subject)
	if !ok || !cat.Expected(l, g.Now()) {
		fail(w, http.StatusForbidden, "location.inactive", "location "+t.Subject+" is not active")
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

// importText takes samples in the Prometheus text format (what linkos-probe pushes, and heartbeats),
// labelled like remote write: a probe's with its location, a host's with its name, a heartbeat's as such.
func (g *gateway) importText(w http.ResponseWriter, r *http.Request, t store.Token) {
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
		g.mu.Lock()
		g.lastWrite[t.Subject] = g.Now()
		g.mu.Unlock()
	} else {
		q.Add("extra_label", "source=heartbeat")
		if t.Subject != "" {
			q.Add("extra_label", "sender="+t.Subject)
		}
	}
	r.URL.Path, r.URL.RawQuery = "/api/v1/import/prometheus", q.Encode()
	g.vm.ServeHTTP(w, r)
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

func (g *gateway) read(w http.ResponseWriter, r *http.Request, _ store.Token) { g.vm.ServeHTTP(w, r) }

// rulesAlerts takes alerts from the rule engine (its own bearer token) or from anyone with alerts:send.
func (g *gateway) rulesAlerts(w http.ResponseWriter, r *http.Request) {
	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if g.RulesToken != "" && secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(g.RulesToken)) == 1 {
		g.alerts(w, r, store.Token{Name: "rules"})
		return
	}
	g.with("alerts", []string{"alerts:send"}, g.alerts).ServeHTTP(w, r)
}

func (g *gateway) alerts(w http.ResponseWriter, r *http.Request, _ store.Token) {
	if g.Alerts == nil {
		fail(w, http.StatusServiceUnavailable, "upstream", "alerts are not taken here")
		return
	}
	var in []router.Postable
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad_input", "the body must be a JSON array of alerts")
		return
	}
	if err := g.Alerts(in); err != nil {
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

func fail(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": errCode})
}
