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
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/testutil"
)

func setupSettings(t *testing.T) (*testutil.Env, *settings.Service, string) {
	t.Helper()
	e := testutil.New(t)
	svc, err := settings.Open(settings.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithConfig(cfg), WithSettings(svc)))
	t.Cleanup(srv.Close)
	return e, svc, srv.URL
}

// The configuration tools carry the settings as their settings kind — the plan's own interface —
// with the scopes, versions, history and revert the REST routes have.
func TestConfigToolsSettingsKind(t *testing.T) {
	e, _, url := setupSettings(t)
	read := connect(t, url, e.Token(t, "reader", "status:read"))
	admin := connect(t, url, e.Token(t, "claude-code", "secrets:admin"))

	if res, text := call(t, read, "get_config", map[string]any{"kind": "settings"}); res.IsError || !strings.Contains(text, `"request.max_uses"`) {
		t.Fatalf("get_config of the settings: %s", text)
	}
	if _, text := call(t, read, "get_config", map[string]any{"kind": "settings", "key": "request.max_uses"}); !strings.Contains(text, `"from":"default"`) || !strings.Contains(text, `"unlimited"`) {
		t.Errorf("one setting: %s", text)
	}
	if res, text := call(t, read, "put_config", map[string]any{"kind": "settings", "key": "request.max_uses", "value": 50, "note": "tighter"}); !res.IsError || !strings.Contains(text, "secrets:admin") {
		t.Errorf("a reader cannot change the settings: %s", text)
	}
	res, text := call(t, admin, "put_config", map[string]any{"kind": "settings", "key": "request.max_uses", "value": 50, "note": "tighter while testing"})
	if res.IsError || !strings.Contains(text, `"version":1`) || !strings.Contains(text, `"action":"set"`) || !strings.Contains(text, `"via":"mcp"`) {
		t.Fatalf("put_config of a setting: %s", text)
	}
	if res, text := call(t, admin, "put_config", map[string]any{"kind": "settings", "key": "request.max_uses", "value": "many", "note": "because it does not fit"}); !res.IsError || !strings.Contains(text, "unlimited") {
		t.Errorf("a value that does not fit: %s", text)
	}
	// resolution through the tool: a tag's row, and the level-by-level answer
	if res, text := call(t, admin, "put_config", map[string]any{"kind": "settings", "key": "request.max_uses", "value": 30,
		"scope": "tag", "target": "env/prod", "note": "prod is tighter"}); res.IsError {
		t.Fatalf("a tag's row: %s", text)
	}
	if _, text := call(t, admin, "get_config", map[string]any{"kind": "settings", "key": "request.max_uses", "tag": []string{"env/prod"}}); !strings.Contains(text, `"from":"tag"`) || !strings.Contains(text, `"target":"env/prod"`) {
		t.Errorf("resolved for a tag: %s", text)
	}
	if _, text := call(t, admin, "list_config_changes", map[string]any{"kind": "settings"}); !strings.Contains(text, `"key":"request.max_uses"`) {
		t.Errorf("list_config_changes of the settings: %s", text)
	}
	if _, text := call(t, admin, "get_config_change", map[string]any{"kind": "settings", "version": 1}); !strings.Contains(text, `"after":50`) {
		t.Errorf("get_config_change of a settings change: %s", text)
	}
	if res, text := call(t, admin, "revert_config_change", map[string]any{"kind": "settings", "version": 1, "note": "because the test reverts"}); res.IsError || !strings.Contains(text, `"reverts":1`) {
		t.Fatalf("revert_config_change of a settings change: %s", text)
	}
	if res, text := call(t, read, "list_config_changes", map[string]any{"kind": "settings"}); !res.IsError {
		t.Errorf("a reader cannot list settings history: %s", text)
	}
	if res, text := call(t, admin, "get_config_change", map[string]any{"version": 1}); !res.IsError && strings.Contains(text, `"key":"request.max_uses"`) {
		t.Errorf("get_config_change without kind must not steal a settings change: %s", text)
	}
	if res, text := call(t, admin, "delete_config", map[string]any{"kind": "settings", "key": "request.max_uses",
		"scope": "tag", "target": "env/prod", "note": "done with the tag"}); res.IsError || !strings.Contains(text, `"action":"reset"`) {
		t.Fatalf("delete_config resets a setting: %s", text)
	}
	if strings.Join(e.Bot.Texts, "\n") == "" || !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "request.max_uses") {
		t.Errorf("echo: %v", e.Bot.Texts)
	}
	if a := e.Store.AuditLog(4); !strings.HasPrefix(a[3].Action, "settings_") || a[3].Via != "mcp" {
		t.Errorf("audit: %+v", a[3])
	}
	// the instructions say the settings are there
	if !strings.Contains(read.InitializeResult().Instructions, "settings") {
		t.Error("the instructions tell of the settings kind")
	}
}

// Without the settings option the tools answer as they always did: the settings kind is refused.
func TestConfigToolsWithoutSettings(t *testing.T) {
	e := testutil.New(t)
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithConfig(cfg)))
	defer srv.Close()
	read := connect(t, srv.URL, e.Token(t, "reader", "status:read"))
	if res, text := call(t, read, "get_config", map[string]any{"kind": "settings"}); !res.IsError || !strings.Contains(text, "settings") {
		t.Errorf("kind settings without the settings: %s", text)
	}
	if tools, err := read.ListTools(context.Background(), nil); err != nil || len(tools.Tools) != 42 {
		t.Errorf("the tool count does not change: %d %v", len(tools.Tools), err)
	}
}
