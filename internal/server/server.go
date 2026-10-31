// Package server routes the service's HTTP requests: health and readiness, metrics (direct
// connections only), Feishu's HTTP callbacks, the drill check, the REST API, the MCP endpoint and the
// status page. Machines (probes, the rule engine) use the gateway's own listener.
package server

import (
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openlinkos/status/internal/api"
	"github.com/openlinkos/status/internal/bridge"
	"github.com/openlinkos/status/internal/ctl"
	"github.com/openlinkos/status/internal/i18n"
)

// Deps are what the routes need. Feishu is nil unless callbacks come over HTTP.
type Deps struct {
	Bridge  *bridge.Bridge
	API     *api.Server
	MCP     http.Handler
	Metrics http.Handler
	Feishu  http.Handler
	Drill   func() time.Time
	Log     *slog.Logger
}

// CSP for everything the service serves itself; the status page will bring its own.
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
	mux.HandleFunc("GET /{$}", placeholder)
	return headers(mux)
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

var page = template.Must(template.New("p").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>LinkOS Status</title><meta name="robots" content="noindex"></head>
<body><main><h1>LinkOS Status</h1><p>{{.EN}}</p><p lang="zh-CN">{{.ZH}}</p></main></body></html>
`))

func placeholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	const msg = "The status page is on its way."
	_ = page.Execute(w, map[string]string{"EN": msg, "ZH": i18n.T(i18n.ZH, "The status page is on its way.")})
}

// DrillFrom reads the drill's end from the store.
func DrillFrom(get func(string) time.Time) func() time.Time {
	return func() time.Time { return get(ctl.DrillKey) }
}
