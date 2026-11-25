// Package server routes the service's HTTP requests: health and readiness, metrics (direct
// connections only), Feishu's HTTP callbacks, the drill check, the REST API, the MCP endpoint and the
// pages (the public ones at the root, the console under /console). Machines (probes, the rule engine)
// use the gateway's own listener.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/exercise"
	"github.com/openlinkos/beacon/internal/oauth"
	"github.com/openlinkos/beacon/internal/web"
)

// Deps are what the routes need. Feishu is nil unless callbacks come over HTTP; Web nil serves only
// the public page at / (without status pages, the console or the pages people open from the chat).
type Deps struct {
	Bridge   *bridge.Bridge
	API      *api.Server
	MCP      http.Handler
	Metrics  http.Handler
	Feishu   http.Handler
	DrillRun func() exercise.Run
	Exercise *exercise.Authority
	Log      *slog.Logger
	Web      *web.Server
	// Standby, when set (a primary/standby pair's lease was not held at the last attempt), turns
	// /healthz to 503: the load balancer or the VIP in front sends its traffic to the primary.
	Standby func() bool
	// Live, when set, is the instance's own health (it can reach PostgreSQL), including a waiting
	// standby. Docker's health check asks /livez so it does not restart a healthy standby.
	// Nil treats the process as live.
	Live func() bool
	// OAuth is Beacon's authorization server for MCP clients: its metadata, registration, token and
	// revocation endpoints (the console serves /oauth/authorize); nil leaves them out.
	OAuth *oauth.Server
}

// CSP for everything the service serves itself; the pages bring their own.
const CSP = "default-src 'none'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler builds the router.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if d.Live != nil && !d.Live() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unhealthy\n"))
			return
		}
		if d.Standby != nil && d.Standby() {
			_, _ = w.Write([]byte("standby\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if d.Standby != nil && d.Standby() {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("standby\n"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		ok, why := d.Bridge.Ready()
		code := http.StatusOK
		if !ok {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": ok, "not_ready": why})
	})
	mux.Handle("GET /metrics", directOnly(d.Metrics))
	if d.Feishu != nil {
		mux.Handle("POST /hooks/feishu", d.Feishu)
	}
	mux.HandleFunc("GET "+exercise.Path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		now := time.Now()
		var run exercise.Run
		if d.DrillRun != nil {
			run = d.DrillRun()
		}
		if run.Active(now) {
			if r.Method == http.MethodGet && d.Exercise != nil {
				if receipt := d.Exercise.Respond(run, r.Header.Get(exercise.ChallengeHeader), r.Host, r.Header.Get(exercise.SampleHeader), now); receipt != "" {
					w.Header().Set(exercise.ReceiptHeader, receipt)
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("drill\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	d.API.Register(mux)
	mux.Handle("/mcp", d.MCP)
	if d.OAuth != nil {
		d.OAuth.Register(mux)
	}
	if d.Web == nil {
		d.Web = &web.Server{Log: d.Log}
	}
	d.Web.Register(mux)              // the pages set their own Content-Security-Policy over the one below
	return headers(d.Web.Hosts(mux)) // a status page on a host name of its own answers there, and nothing else does
}

// directOnly answers only requests that did not come through a proxy (the edge adds X-Forwarded-For).
func directOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Real-IP") != "" {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		if !strings.HasPrefix(r.URL.Path, "/mcp") {
			hd.Set("Content-Security-Policy", CSP)
		}
		h.ServeHTTP(w, r)
	})
}
