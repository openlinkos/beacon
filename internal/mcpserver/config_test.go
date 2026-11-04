package mcpserver

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestConfigTools(t *testing.T) {
	e := testutil.New(t)
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithConfig(cfg)))
	defer srv.Close()

	read := connect(t, srv.URL, e.Token(t, "reader", "status:read"))
	tools, err := read.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 42 {
		t.Fatalf("tools: %d %v", len(tools.Tools), err)
	}
	if !strings.Contains(read.InitializeResult().Instructions, "put_config") {
		t.Error("the instructions say how to change the configuration")
	}
	if res, text := call(t, read, "put_config", map[string]any{"kind": "locations", "key": "edge-1", "value": map[string]any{"name": "x", "region": "overseas"}}); !res.IsError || !strings.Contains(text, "config:write") {
		t.Errorf("a read token cannot change it: %s", text)
	}

	w := connect(t, srv.URL, e.Token(t, "codex", "config:write"))
	res, text := call(t, w, "put_config", map[string]any{"kind": "locations", "key": "edge-1", "note": "new node",
		"value": map[string]any{"name": "United States - Los Angeles", "region": "overseas", "probe": map[string]any{"timeout": "30s"}}})
	if res.IsError || !strings.Contains(text, `"version":1`) || !strings.Contains(text, `"via":"mcp"`) || !strings.Contains(text, `"op":"added"`) {
		t.Fatalf("put_config: %s", text)
	}
	if _, ok := cfg.Catalog().Location("edge-1"); !ok {
		t.Error("in force at once")
	}
	if res, text := call(t, w, "put_config", map[string]any{"kind": "locations", "key": "edge-2", "value": map[string]any{"name": "x", "region": "earth"}}); !res.IsError || !strings.Contains(text, "region must be overseas or mainland") {
		t.Errorf("checked as the files are: %s", text)
	}
	if res, text := call(t, w, "put_config", map[string]any{"kind": "targets", "key": "edge-1-local", "value": []any{map[string]any{"targets": []string{"https://local.example/"}, "labels": map[string]string{"module": "http_head", "service": "local"}}}}); res.IsError {
		t.Fatalf("a target set: %s", text)
	}
	if _, text := call(t, read, "get_config", map[string]any{"kind": "targets"}); !strings.Contains(text, `"probed_by":"edge-1"`) || strings.Contains(text, `"locations"`) {
		t.Errorf("get_config of one kind: %s", text)
	}
	if _, text := call(t, read, "get_config", nil); !strings.Contains(text, `"locations":[{"id":"edge-1"`) || !strings.Contains(text, `"modules":{}`) {
		t.Errorf("get_config: %s", text)
	}
	if res, text := call(t, read, "get_config", map[string]any{"kind": "locations", "key": "nope"}); !res.IsError || !strings.Contains(text, "not found") {
		t.Errorf("an unknown item: %s", text)
	}
	if _, text := call(t, read, "list_config_changes", map[string]any{"kind": "locations"}); !strings.Contains(text, `"version":1`) || strings.Contains(text, `"version":2`) || strings.Contains(text, `"after"`) {
		t.Errorf("list_config_changes: %s", text)
	}
	if _, text := call(t, read, "get_config_change", map[string]any{"version": 1}); !strings.Contains(text, `"after":{"id":"edge-1"`) || !strings.Contains(text, `"note":"new node"`) {
		t.Errorf("get_config_change: %s", text)
	}
	if res, text := call(t, w, "revert_config_change", map[string]any{"version": 1}); !res.IsError || !strings.Contains(text, "edge-1-local.yaml") {
		t.Errorf("reverting the location while its set exists fails the checks: %s", text)
	}
	if res, text := call(t, w, "delete_config", map[string]any{"kind": "targets", "key": "edge-1-local"}); res.IsError {
		t.Fatalf("delete_config: %s", text)
	}
	if res, text := call(t, w, "revert_config_change", map[string]any{"version": 1, "note": "wrong"}); res.IsError || !strings.Contains(text, `"reverts":1`) {
		t.Fatalf("revert_config_change: %s", text)
	}
	if _, ok := cfg.Catalog().Location("edge-1"); ok {
		t.Error("reverted")
	}
	if a := e.Store.AuditLog(1); a[0].Action != "config_revert" || a[0].Via != "mcp" || a[0].Actor != "codex" {
		t.Errorf("audit: %+v", a[0])
	}
	if last := e.Bot.Texts[len(e.Bot.Texts)-1]; last != "codex reverted configuration change 1 via MCP: 0 added, 0 changed, 1 removed (version 4)\nNote: wrong" {
		t.Errorf("echo: %q", last)
	}
}
