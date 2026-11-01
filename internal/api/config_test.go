package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/testutil"
)

func setupConfig(t *testing.T) (*testutil.Env, client) {
	e := testutil.New(t)
	mux := http.NewServeMux()
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	(&Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Config: cfg}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return e, client{t, srv}
}

func (c client) raw(method, path, token string) (int, string) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestConfigRoutes(t *testing.T) {
	e, c := setupConfig(t)
	read := e.Token(t, "reader", "status:read")
	write := e.Token(t, "claude-code", "config:write")

	if code, out := c.do("GET", "/api/v1/config", read, ""); code != 200 || out["version"] != float64(0) {
		t.Fatalf("an empty configuration: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/config/locations/edge-1", read, `{"name": "United States - Los Angeles", "region": "overseas"}`); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("a read token cannot change it: %d %v", code, out)
	}
	code, out := c.do("PUT", "/api/v1/config/locations/edge-1?note=new+node", write, `{"name": "United States - Los Angeles", "region": "overseas", "probe": {"retries": 1}}`)
	items, _ := out["items"].([]any)
	if code != 200 || out["version"] != float64(1) || out["via"] != "api" || out["actor"] != "claude-code" || len(items) != 1 {
		t.Fatalf("put: %d %v", code, out)
	}
	if it := items[0].(map[string]any); it["kind"] != "location" || it["key"] != "edge-1" || it["op"] != "added" || it["after"] == nil {
		t.Errorf("item: %v", it)
	}
	if code, body := c.raw("GET", "/api/v1/config/locations/edge-1", write); code != 200 || !strings.Contains(body, `"probe": {`) || !strings.Contains(body, `"retries": 1`) {
		t.Errorf("a config:write token reads what it changes: %d %s", code, body)
	}
	if code, out := c.do("GET", "/api/v1/config/locations", read, ""); code != 200 || len(out["locations"].([]any)) != 1 || out["heartbeats"] != nil {
		t.Errorf("one kind: %d %v", code, out)
	}
	if !slices.ContainsFunc(e.Bot.Texts, func(s string) bool {
		return s == "claude-code added location edge-1 via API (version 1)\nNote: new node"
	}) {
		t.Errorf("echo: %v", e.Bot.Texts)
	}
	if a := e.Store.AuditLog(1); a[0].Action != "config_put" || a[0].Via != "api" {
		t.Errorf("audit: %+v", a[0])
	}

	for name, tc := range map[string]struct {
		method, path, body string
		status             int
		code               string
	}{
		"invalid":       {"PUT", "/api/v1/config/locations/edge-2", `{"name": "x", "region": "earth"}`, 422, "config.invalid"},
		"unknown field": {"PUT", "/api/v1/config/heartbeats/nas", `{"max_age": "26h", "severity": "warning", "owner": "alex"}`, 400, "bad_input"},
		"set prefix":    {"PUT", "/api/v1/config/targets/us-web", `[{"targets": ["https://x/"], "labels": {"module": "m", "service": "s"}}]`, 422, "config.invalid"},
		"not found":     {"DELETE", "/api/v1/config/resources/nothing", ``, 404, "config.not_found"},
		"no version":    {"GET", "/api/v1/config/history/99", ``, 404, "config.not_found"},
		"bad version":   {"POST", "/api/v1/config/history/x/revert", ``, 400, "bad_input"},
		"bad limit":     {"GET", "/api/v1/config/history?limit=0", ``, 400, "bad_input"},
		"bad kind":      {"GET", "/api/v1/config/history?kind=robots", ``, 400, "bad_input"},
	} {
		if code, out := c.do(tc.method, tc.path, write, tc.body); code != tc.status || out["code"] != tc.code {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}

	// a target set by its file name, and a module
	if code, out := c.do("PUT", "/api/v1/config/targets/all-web.yaml", write, `[{"targets": ["https://linkos.org/"], "labels": {"module": "http_head", "service": "linkos.org"}}]`); code != 200 {
		t.Fatalf("target set: %d %v", code, out)
	}
	if code, body := c.raw("GET", "/api/v1/config/targets/all-web", read); code != 200 || !strings.HasPrefix(body, "[") || !strings.Contains(body, "linkos.org") {
		t.Errorf("a set's groups: %d %s", code, body)
	}
	if code, out := c.do("GET", "/api/v1/config/targets", read, ""); code != 200 || out["targets"].([]any)[0].(map[string]any)["probed_by"] != "all" {
		t.Errorf("sets: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/config/modules/http_head", write, `{"prober": "http", "timeout": "20s", "http": {"method": "HEAD"}}`); code != 200 {
		t.Fatalf("module: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/config/modules/http_head", read, ""); code != 200 || out["timeout"] != "20s" {
		t.Errorf("module: %d %v", code, out)
	}

	// history, one change, revert
	code, out = c.do("GET", "/api/v1/config/history?kind=locations&key=edge-1", read, "")
	changes, _ := out["changes"].([]any)
	if code != 200 || len(changes) != 1 || changes[0].(map[string]any)["items"].([]any)[0].(map[string]any)["after"] != nil {
		t.Fatalf("history of one item, without values: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/config/history?limit=1", read, ""); code != 200 || len(out["changes"].([]any)) != 1 || out["changes"].([]any)[0].(map[string]any)["version"] != float64(3) {
		t.Errorf("latest change: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/config/history/1", read, ""); code != 200 || out["items"].([]any)[0].(map[string]any)["after"] == nil {
		t.Errorf("one change: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/config/history/1/revert", read, ""); code != 403 {
		t.Errorf("a read token cannot revert: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/config/history/1/revert?note=wrong+node", write, ""); code != 200 || out["reverts"] != float64(1) || out["action"] != "revert" {
		t.Fatalf("revert: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/v1/config/locations/edge-1", read, ""); code != 404 {
		t.Errorf("reverted: %d", code)
	}
	if code, out := c.do("POST", "/api/v1/config/history/1/revert", write, ""); code != 409 || out["code"] != "config.conflict" {
		t.Errorf("reverting twice: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/config/targets/all-web?note=gone", write, ""); code != 200 || out["action"] != "delete" || out["note"] != "gone" {
		t.Errorf("delete: %d %v", code, out)
	}
}

// TestConfigErrorCodes keeps the codes the configuration's routes answer with in the description.
func TestConfigErrorCodes(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas struct {
				Error struct {
					Properties struct {
						Code struct {
							Enum []string `yaml:"enum"`
						} `yaml:"code"`
					} `yaml:"properties"`
				} `yaml:"Error"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"config.not_found", "config.invalid", "config.conflict", "bad_input"} {
		if !slices.Contains(doc.Components.Schemas.Error.Properties.Code.Enum, code) {
			t.Errorf("%s is not among the error codes in openapi.yaml", code)
		}
	}
}
