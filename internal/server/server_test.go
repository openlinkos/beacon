package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/status/internal/api"
	"github.com/openlinkos/status/internal/metrics"
	"github.com/openlinkos/status/internal/testutil"
)

func TestRoutes(t *testing.T) {
	e := testutil.New(t)
	drill := time.Time{}
	h := Handler(Deps{
		Bridge: e.Bridge, API: &api.Server{Bridge: e.Bridge, Auth: &api.Auth{Store: e.Store}, Log: slog.Default()},
		MCP: http.NotFoundHandler(), Metrics: metrics.New("test"), WebhookToken: "0123456789abcdef0123456789",
		Drill: func() time.Time { return drill }, Log: slog.Default(),
	})
	do := func(method, path, auth, body string, hdr ...string) (int, string, http.Header) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, string(b), rec.Header()
	}

	if code, _, hd := do("GET", "/healthz", "", ""); code != 200 || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Content-Security-Policy") == "" {
		t.Errorf("healthz: %d %v", code, hd)
	}
	if code, body, _ := do("GET", "/readyz", "", ""); code != 200 || !strings.Contains(body, `"ready":true`) {
		t.Errorf("readyz: %d %s", code, body)
	}
	if code, body, _ := do("GET", "/metrics", "", ""); code != 200 || !strings.Contains(body, "linkos_status_build_info") {
		t.Errorf("metrics direct: %d", code)
	}
	if code, _, _ := do("GET", "/metrics", "", "", "X-Forwarded-For", "198.51.100.7"); code != 404 {
		t.Errorf("metrics through the edge: %d", code)
	}

	hook := `{"version":"4","groupKey":"k","status":"firing","commonLabels":{"severity":"warning"},"alerts":[]}`
	if code, _, _ := do("POST", "/hooks/alertmanager", "", hook); code != 401 {
		t.Errorf("webhook without the token: %d", code)
	}
	if code, _, _ := do("POST", "/hooks/alertmanager", "Bearer wrong", hook); code != 401 {
		t.Errorf("webhook with a wrong token: %d", code)
	}
	if code, _, _ := do("POST", "/hooks/alertmanager", "Bearer 0123456789abcdef0123456789", "{}"); code != 400 {
		t.Errorf("bad payload: %d", code)
	}
	if code, _, _ := do("POST", "/hooks/alertmanager", "Bearer 0123456789abcdef0123456789", hook); code != 200 {
		t.Errorf("webhook: %d", code)
	}
	if code, _, _ := do("POST", "/hooks/feishu", "", "{}"); code != 404 && code != 405 {
		t.Errorf("feishu callbacks are off in websocket mode: %d", code)
	}

	if code, _, _ := do("HEAD", "/drill/healthz", "", ""); code != 200 {
		t.Errorf("drill off: %d", code)
	}
	drill = time.Now().Add(time.Minute)
	if code, _, _ := do("GET", "/drill/healthz", "", ""); code != 503 {
		t.Errorf("drill on: %d", code)
	}

	if code, body, _ := do("GET", "/", "", ""); code != 200 || !strings.Contains(body, "on its way") || strings.Contains(body, "<script") || strings.Contains(body, "style=") {
		t.Errorf("page: %d %s", code, body)
	}
	if code, _, _ := do("GET", "/nope", "", ""); code != 404 {
		t.Errorf("unknown path: %d", code)
	}
	if code, _, _ := do("GET", "/api/v1/alerts", "", ""); code != 401 {
		t.Errorf("api is mounted: %d", code)
	}
}
