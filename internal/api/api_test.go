package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/openlinkos/beacon/internal/testutil"
)

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func (c client) do(method, path, token, body string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func setup(t *testing.T) (*testutil.Env, client) {
	e := testutil.New(t)
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default()}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return e, client{t, srv}
}

func TestAuth(t *testing.T) {
	e, c := setup(t)
	read := e.Token(t, "reader", "status:read")
	if code, _ := c.do("GET", "/api/v1/openapi.yaml", "", ""); code != 200 {
		t.Errorf("the description is public: %d", code)
	}
	if code, out := c.do("GET", "/api/v1/alerts", "", ""); code != 401 || out["code"] != "auth.required" {
		t.Errorf("no token: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/alerts", "lbt_wrong", ""); code != 401 || out["code"] != "auth.invalid" {
		t.Errorf("bad token: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/alerts/"+testutil.GroupID()+"/ack", read, ""); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("read token writing: %d %v", code, out)
	}
	for range 20 {
		c.do("GET", "/api/v1/alerts", "lbt_wrong", "")
	}
	if code, out := c.do("GET", "/api/v1/alerts", read, ""); code != 429 || out["code"] != "auth.too_many_failures" {
		t.Errorf("after many failures even a good token waits: %d %v", code, out)
	}
}

func TestOperations(t *testing.T) {
	e, c := setup(t)
	read := e.Token(t, "reader", "status:read")
	write := e.Token(t, "claude-code", "status:read", "alerts:write")
	id := testutil.GroupID()

	code, out := c.do("GET", "/api/v1/alerts", read, "")
	alerts, _ := out["alerts"].([]any)
	if code != 200 || len(alerts) != 1 || alerts[0].(map[string]any)["id"] != id {
		t.Fatalf("alerts: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/v1/alerts?state=odd", read, ""); code != 400 {
		t.Errorf("bad state: %d", code)
	}
	if code, out := c.do("GET", "/api/v1/alerts/nope", read, ""); code != 404 || out["code"] != "alert.not_found" {
		t.Errorf("unknown alert: %d %v", code, out)
	}
	code, out = c.do("POST", "/api/v1/alerts/"+id+"/ack", write, `{"note":"looking"}`)
	if ack, _ := out["ack"].(map[string]any); code != 200 || ack["via"] != "api" || ack["by"] != "claude-code" {
		t.Fatalf("ack: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/silences", write, `{"alert":"`+id+`","duration":"1h"}`); code != 400 || out["code"] != "bad_input" {
		t.Errorf("silence without a comment: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/api/v1/silences", write, `{"alert":"`+id+`","duration":"1h","comment":"x","extra":1}`); code != 400 {
		t.Errorf("unknown fields are refused: %d", code)
	}
	code, out = c.do("POST", "/api/v1/silences", write, `{"alert":"`+id+`","duration":"1h","comment":"harbor upgrade"}`)
	if code != 201 || out["silence"] == nil || len(e.AM.Created) != 1 {
		t.Fatalf("silence: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/v1/silences", read, ""); code != 200 {
		t.Errorf("silences: %d", code)
	}
	if code, _ := c.do("DELETE", "/api/v1/silences/sil-1", write, ""); code != 204 || len(e.AM.Expired) != 1 {
		t.Errorf("expire: %d", code)
	}
	code, out = c.do("GET", "/api/v1/audit?limit=10", read, "")
	if entries, _ := out["entries"].([]any); code != 200 || len(entries) != 3 {
		t.Errorf("audit: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/bridge", read, ""); code != 200 || out["firing"] == nil {
		t.Errorf("bridge: %d %v", code, out)
	}
	if len(e.Bot.Texts) < 4 { // the alert itself, then ack, silence and expire echoed
		t.Errorf("echoes: %v", e.Bot.Texts)
	}
}

// TestOpenAPIInStep keeps the description and the routes together.
func TestOpenAPIInStep(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for p, ops := range doc.Paths {
		for m := range ops {
			documented[strings.ToUpper(m)+" /api/v1"+p] = true
		}
	}
	for _, r := range Routes {
		if !documented[r] {
			t.Errorf("%s is not in openapi.yaml", r)
		}
		delete(documented, r)
	}
	delete(documented, "GET /api/v1/openapi.yaml")
	for r := range documented {
		t.Errorf("%s is documented but not served", r)
	}
}
