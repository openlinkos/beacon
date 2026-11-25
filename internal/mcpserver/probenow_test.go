package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/targets"
	"github.com/openlinkos/beacon/internal/testutil"
	"github.com/openlinkos/beacon/internal/web/link"
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

func TestProbeNowRejectsEarlierEpisodeAcrossRESTAndMCP(t *testing.T) {
	board := testutil.ProbeBoard(t)
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.ProbeNow = board })
	secret := e.Token(t, "recovery", "status:read", "alerts:write")
	auth := &api.Auth{Store: e.Store}
	mux := http.NewServeMux()
	(&api.Server{Bridge: e.Bridge, Auth: auth, Log: slog.Default()}).Register(mux)
	mux.Handle("/mcp", Handler(e.Bridge, SSH{}, auth, slog.Default()))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := connect(t, srv.URL+"/mcp", secret)
	request := func(method, path, in string, out any) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	group := testutil.GroupID()
	path := "/api/v1/alerts/" + group
	var observed bridge.GroupView
	if status := request(http.MethodGet, path, "", &observed); status != http.StatusOK || !link.ValidEpisode(observed.Episode) {
		t.Fatalf("REST alert did not expose its episode: %d, %q", status, observed.Episode)
	}
	var evidence bridge.Evidence
	if status := request(http.MethodGet, path+"/evidence", "", &evidence); status != http.StatusOK || evidence.Alert.Episode != observed.Episode {
		t.Fatalf("REST evidence did not expose the same episode: %d, %q", status, evidence.Alert.Episode)
	}
	for _, tool := range []string{"get_evidence", "get_timeline"} {
		if result, wire := call(t, c, tool, map[string]any{"id": group}); result.IsError || !strings.Contains(wire, `"episode":"`+observed.Episode+`"`) {
			t.Fatalf("%s did not expose the same episode: %s", tool, wire)
		}
	}

	received := alertmanager.Webhook{GroupKey: testutil.GroupKey, Status: "resolved", CommonLabels: observed.Labels}
	if err := e.Bridge.HandleWebhook(t.Context(), received); err != nil {
		t.Fatal(err)
	}
	received.Status = "firing"
	if err := e.Bridge.HandleWebhook(t.Context(), received); err != nil {
		t.Fatal(err)
	}
	current, ok := e.Store.Group(group)
	if !ok || current.Episode == observed.Episode || !link.ValidEpisode(current.Episode) {
		t.Fatalf("fixture did not replace the episode: %q", current.Episode)
	}

	const route = "/api/v1/probe-now"
	input := `{"alert_id":"` + group + `","expected_episode":"` + observed.Episode + `","wait":1}`
	var problem map[string]string
	if status := request(http.MethodPost, route, input, &problem); status != http.StatusConflict || problem["code"] != "incident.changed" {
		t.Fatalf("REST accepted a check for the previous episode: %d %v", status, problem)
	}
	if result, wire := call(t, c, "probe_now", map[string]any{"alert_id": group, "expected_episode": observed.Episode, "wait": 1}); !result.IsError || !strings.Contains(wire, "incident.changed:") {
		t.Fatalf("MCP accepted a check for the previous episode: %s", wire)
	}
	if queued := board.Wait(t.Context(), "edge-1", 0); len(queued) != 0 {
		t.Fatalf("a stale request reached a probe: %+v", queued)
	}
	after, _ := e.Store.Group(group)
	if len(after.ProbeRuns) != 0 || after.PassingCheck != nil {
		t.Fatalf("a stale request verified the replacement episode: %v, %+v", after.ProbeRuns, after.PassingCheck)
	}

	for _, bad := range []string{`{"service":"hub-example-com","expected_episode":"` + current.Episode + `"}`, `{"alert_id":"` + group + `","expected_episode":"invalid"}`} {
		if status := request(http.MethodPost, route, bad, &problem); status != http.StatusBadRequest || problem["code"] != "bad_input" {
			t.Fatalf("unusable episode precondition was accepted: %d %v", status, problem)
		}
	}
	if result, wire := call(t, c, "probe_now", map[string]any{"alert_id": group, "expected_episode": current.Episode, "wait": 1}); result.IsError || !strings.Contains(wire, `"episode":"`+current.Episode+`"`) {
		t.Fatalf("matching episode: %s", wire)
	}
	checked, _ := e.Store.Group(group)
	if len(checked.ProbeRuns) != 1 {
		t.Fatalf("only the matching request should be recorded: %+v", checked.ProbeRuns)
	}
}
