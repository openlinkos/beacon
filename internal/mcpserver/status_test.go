package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestStatusTools(t *testing.T) {
	e := testutil.New(t)
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	page := `{"title": "Example Status", "components": [{"name": "hub", "title": "Hub", "services": ["hub-linkos-io"]}]}`
	if _, err := cfg.Put(context.Background(), configdb.Actor{Name: "test", Via: "cli"}, "status_pages", "public", json.RawMessage(page), ""); err != nil {
		t.Fatal(err)
	}
	pages := status.New(status.Deps{Config: cfg, Bridge: e.Bridge, Store: e.Store})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithConfig(cfg), WithStatus(pages)))
	defer srv.Close()

	read := connect(t, srv.URL, e.Token(t, "reader", "status:read"))
	tools, err := read.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 43 {
		t.Fatalf("tools: %d %v", len(tools.Tools), err)
	}
	if !strings.Contains(read.InitializeResult().Instructions, "schedule_maintenance") {
		t.Error("the instructions say how incidents and maintenance reach the pages")
	}
	res, text := call(t, read, "get_status_page", map[string]any{"name": "public"})
	if res.IsError || !strings.Contains(text, `"status":"down"`) || !strings.Contains(text, `"title":"Outage: Hub"`) {
		t.Errorf("get_status_page: %s", text)
	}
	if res, text := call(t, read, "get_status_page", map[string]any{"name": "nope"}); !res.IsError || !strings.Contains(text, "status_pages") {
		t.Errorf("an unknown page: %s", text)
	}
	if res, text := call(t, read, "schedule_maintenance", map[string]any{"services": []string{"hub-linkos-io"}, "ends_at": "2030-01-01T00:00:00Z", "title": "x"}); !res.IsError || !strings.Contains(text, "maintenance:write") {
		t.Errorf("a read token plans nothing: %s", text)
	}

	agent := connect(t, srv.URL, e.Token(t, "claude-oncall", "maintenance:write", "status:read"))
	starts := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	ends := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	res, text = call(t, agent, "schedule_maintenance", map[string]any{"services": []string{"hub-linkos-io"}, "starts_at": starts, "ends_at": ends,
		"title": "Hub upgrade", "description": "Pulls may fail."})
	if res.IsError || !strings.Contains(text, `"title":"Hub upgrade"`) || !strings.Contains(text, `"via":"mcp"`) {
		t.Fatalf("schedule_maintenance: %s", text)
	}
	var m struct {
		ID string `json:"id"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &m)
	if res, text := call(t, agent, "schedule_maintenance", map[string]any{"services": []string{"hub-linkos-io"}, "ends_at": "tomorrow", "title": "x"}); !res.IsError || !strings.Contains(text, "RFC 3339") {
		t.Errorf("a time that is not one: %s", text)
	}
	if _, text := call(t, read, "list_maintenance", nil); !strings.Contains(text, m.ID) {
		t.Errorf("list_maintenance: %s", text)
	}
	if res, text := call(t, agent, "end_maintenance", map[string]any{"id": m.ID}); res.IsError || !strings.Contains(text, `"cancelled":true`) {
		t.Errorf("end_maintenance: %s", text)
	}
}
