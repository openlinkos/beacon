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
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/web"
)

// Deps are what the routes need. Feishu is nil unless callbacks come over HTTP; Web nil serves only
// the public page at / (without status pages, the console or the pages people open from the chat).
type Deps struct {
	Bridge  *bridge.Bridge
	API     *api.Server
	MCP     http.Handler
	Metrics http.Handler
	Feishu  http.Handler
	Drill   func() time.Time
	Log     *slog.Logger
	Web     *web.Server
}

// CSP for everything the service serves itself; the pages bring their own.
const CSP = "default-src 'none'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler builds the router.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
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
	mux.HandleFunc("GET /drill/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if d.Drill != nil && time.Now().Before(d.Drill()) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("drill\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	d.API.Register(mux)
	mux.Handle("/mcp", d.MCP)
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

// DrillFrom reads the drill's end from the store.
func DrillFrom(get func(string) time.Time) func() time.Time {
	return func() time.Time { return get(ctl.DrillKey) }
}
