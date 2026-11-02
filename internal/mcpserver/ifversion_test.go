package mcpserver

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestConfigToolsIfVersion(t *testing.T) {
	e := testutil.New(t)
	cfg := configdb.Open(configdb.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithConfig(cfg)))
	defer srv.Close()
	w := connect(t, srv.URL, e.Token(t, "codex", "config:write"))
	loc := map[string]any{"name": "United States - Los Angeles", "region": "overseas"}
	if res, text := call(t, w, "put_config", map[string]any{"kind": "locations", "key": "edge-1", "value": loc}); res.IsError || !strings.Contains(text, `"version":1`) {
		t.Fatalf("put_config: %s", text)
	}
	if res, text := call(t, w, "put_config", map[string]any{"kind": "locations", "key": "edge-1", "value": loc, "if_version": 1}); res.IsError || !strings.Contains(text, `"unchanged":true`) {
		t.Fatalf("the same value on the version in force changes nothing: %s", text)
	}
	loc["network"] = "CN2 GIA"
	if res, text := call(t, w, "put_config", map[string]any{"kind": "locations", "key": "edge-1", "value": loc, "if_version": 1}); res.IsError || !strings.Contains(text, `"version":2`) {
		t.Fatalf("put_config on the version in force: %s", text)
	}
	if res, text := call(t, w, "delete_config", map[string]any{"kind": "locations", "key": "edge-1", "if_version": 1}); !res.IsError || !strings.Contains(text, "version 2 is in force, not 1") {
		t.Errorf("a stale delete_config: %s", text)
	}
	if res, text := call(t, w, "delete_config", map[string]any{"kind": "locations", "key": "edge-1", "if_version": 2}); res.IsError || !strings.Contains(text, `"version":3`) {
		t.Errorf("delete_config on the version in force: %s", text)
	}
}
