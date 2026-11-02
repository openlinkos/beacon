package mcpserver

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestProbeNowTool(t *testing.T) {
	board := testutil.ProbeBoard(t)
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.ProbeNow = board })
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default()))
	defer srv.Close()

	read := connect(t, srv.URL, e.Token(t, "reader", "status:read"))
	if res, text := call(t, read, "probe_now", map[string]any{"target": "https://hub.example.com/"}); !res.IsError || !strings.Contains(text, "alerts:write") {
		t.Errorf("a read token cannot probe: %s", text)
	}
	write := connect(t, srv.URL, e.Token(t, "claude-code", "status:read", "alerts:write"))
	if res, text := call(t, write, "probe_now", map[string]any{"service": "nope", "wait": 1}); !res.IsError || !strings.Contains(text, "no target set has service nope") {
		t.Errorf("unknown service: %s", text)
	}
	// edge-1 answers; edge-2 does not in time
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, r := range board.Wait(ctx, "edge-1", 5*time.Second) {
			_ = board.Answer(r.ID, "edge-1", []targets.TargetResult{{Target: "https://hub.example.com/", Module: "http_head", Success: true, DurationSeconds: 0.3, HTTPStatus: 200, Attempts: 1, CertExpiry: 1800000000}})
		}
	}()
	res, text := call(t, write, "probe_now", map[string]any{"target": "https://hub.example.com/", "module": "http_head", "wait": 1})
	if res.IsError {
		t.Fatalf("probe_now: %s", text)
	}
	for _, want := range []string{`"location":"edge-1"`, `"ok":true`, `"http_status":200`, `"cert_expires":"2027-01-15T08:00:00Z"`, `"missing":["edge-2"]`,
		`"complete":false`, `"summary":"1 OK, 1 did not answer"`, `"locations":["edge-1","edge-2"]`, `"untrusted"`} {
		if !strings.Contains(text, want) {
			t.Errorf("probe_now lacks %s: %s", want, text)
		}
	}
	if last := e.Bot.Texts[len(e.Bot.Texts)-1]; last != "claude-code had 2 locations probe https://hub.example.com/ again via MCP: 1 OK, 1 did not answer" {
		t.Errorf("echo %q", last)
	}
	if a := e.Store.AuditLog(1); a[0].Action != "probe_now" || a[0].Via != "mcp" {
		t.Errorf("audit %+v", a[0])
	}
	if res, text := call(t, write, "probe_now", map[string]any{"target": "https://hub.example.com/", "wait": 1}); !res.IsError || !strings.Contains(text, "busy") {
		t.Errorf("the same target at once: %s", text)
	}
}
