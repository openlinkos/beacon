package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestStatusRoutes(t *testing.T) {
	e := testutil.New(t)
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	pages := status.New(status.Deps{Config: cfg, Bridge: e.Bridge, Store: e.Store, PublicURL: "https://beacon.example.com"})
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Config: cfg, Status: pages}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := client{t, srv}
	read := e.Token(t, "reader", "status:read")
	write := e.Token(t, "claude-code", "config:write")
	agent := e.Token(t, "claude-oncall", "maintenance:write", "status:read")

	if code, out := c.do("GET", "/api/v1/status/public", read, ""); code != 404 || out["code"] != "status.not_found" {
		t.Errorf("no page yet: %d %v", code, out)
	}
	page := `{"title": "Example Status", "components": [{"name": "hub", "title": "Hub", "services": ["hub-linkos-io"]}]}`
	if code, out := c.do("PUT", "/api/v1/config/status_pages/public", write, page); code != 200 || out["version"] != float64(1) {
		t.Fatalf("put a page: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/config/status_pages", read, ""); code != 200 || len(out["status_pages"].([]any)) != 1 {
		t.Errorf("list pages: %d %v", code, out)
	}
	code, out := c.do("GET", "/api/v1/status/public", read, "")
	if code != 200 || out["name"] != "public" || out["status"] != "down" || out["url"] != "https://beacon.example.com/status/public" {
		t.Fatalf("the page, while the hub's ServiceDown fires: %d %v", code, out)
	}
	if in := out["incidents"].([]any); len(in) != 1 || in[0].(map[string]any)["id"] != testutil.GroupID() {
		t.Errorf("incidents: %v", in)
	}
	if code, out := c.do("GET", "/api/v1/status/public", write, ""); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("status:read is needed: %d %v", code, out)
	}

	// maintenance: planned, listed, ended, and not twice
	starts := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	ends := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"services": ["hub-linkos-io"], "starts_at": "` + starts + `", "ends_at": "` + ends + `", "title": "Hub upgrade"}`
	if code, out := c.do("POST", "/api/v1/maintenance", read, body); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("maintenance:write is needed: %d %v", code, out)
	}
	code, out = c.do("POST", "/api/v1/maintenance", agent, body)
	if code != 201 || out["title"] != "Hub upgrade" || out["created_by"] != "claude-oncall" || len(out["silences"].([]any)) != 1 {
		t.Fatalf("plan: %d %v", code, out)
	}
	id, _ := out["id"].(string)
	if len(e.AM.Created) != 1 || e.AM.Created[0]["service"] != "hub-linkos-io" {
		t.Errorf("silences: %v", e.AM.Created)
	}
	if !strings.Contains(strings.Join(e.Bot.Texts, "\n"), `claude-oncall planned maintenance "Hub upgrade" of hub-linkos-io`) {
		t.Errorf("echo: %v", e.Bot.Texts)
	}
	if code, out := c.do("GET", "/api/v1/maintenance", read, ""); code != 200 || len(out["maintenance"].([]any)) != 1 {
		t.Errorf("list: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/maintenance", agent, `{"services": [], "ends_at": "`+ends+`", "title": "x"}`); code != 400 || out["code"] != "bad_input" {
		t.Errorf("no services: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/maintenance/"+id, agent, ""); code != 200 || out["cancelled"] != true {
		t.Errorf("called off: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/maintenance/"+id, agent, ""); code != 409 || out["code"] != "maintenance.ended" {
		t.Errorf("twice: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/maintenance/m-nope", agent, ""); code != 404 || out["code"] != "maintenance.not_found" {
		t.Errorf("unknown: %d %v", code, out)
	}
}
