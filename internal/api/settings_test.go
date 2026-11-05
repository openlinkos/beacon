package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/testutil"
)

func setupSettings(t *testing.T) (*testutil.Env, client, *settings.Service) {
	e := testutil.New(t)
	svc, err := settings.Open(settings.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Settings: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return e, client{t, srv}, svc
}

// The settings' routes: read whole and one, changed and reset, history and revert, through the
// scopes the rest of the API uses — and a change is audited and echoed like every other.
func TestSettingsRoutes(t *testing.T) {
	e, c, _ := setupSettings(t)
	read := e.Token(t, "reader", "status:read")
	admin := e.Token(t, "claude-code", "secrets:admin")

	if code, out := c.do("GET", "/api/v1/settings", read, ""); code != 200 {
		t.Fatalf("list: %d %v", code, out)
	} else if list := out["settings"].([]any); len(list) < 60 {
		t.Fatalf("the plan's settings are not all listed: %d", len(list))
	}
	if code, out := c.do("GET", "/api/v1/settings/request.max_uses", read, ""); code != 200 ||
		out["from"] != "default" || out["default"] != "unlimited" || out["value"] != "unlimited" {
		t.Fatalf("one setting: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/settings/no.such.key", read, ""); code != 404 || out["code"] != "settings.unknown" {
		t.Fatalf("unknown key: %d %v", code, out)
	}

	if code, out := c.do("PUT", "/api/v1/settings/request.max_uses", read, `{"value": 50, "reason": "tighter while we test"}`); code != 403 || out["code"] != "auth.scope" {
		t.Fatalf("a reader cannot change: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/settings/request.max_uses", admin, `{"value": 50, "reason": "tighter while we test"}`); code != 200 || out["version"] != float64(1) || out["action"] != "set" {
		t.Fatalf("put: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/settings/request.max_uses", read, ""); code != 200 || out["value"] != float64(50) ||
		out["from"] != "system" || out["actor"] != "claude-code" || out["reason"] != "tighter while we test" {
		t.Fatalf("after put: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/settings/request.max_uses", admin, `{"value": "many", "reason": "because it does not fit"}`); code != 422 || out["code"] != "settings.invalid" {
		t.Fatalf("a value that does not fit: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/settings/request.max_uses", admin, `{"value": 60, "reason": "because the version is stale", "if_version": 99}`); code != 409 || out["code"] != "settings.conflict" {
		t.Fatalf("a stale version: %d %v", code, out)
	}

	// a row at another scope: what a tag holds, and the resolution that places it
	if code, tagOut := c.do("PUT", "/api/v1/settings/request.max_uses", admin, `{"value": 30, "scope": "tag", "target": "env/prod", "reason": "prod is tighter"}`); code != 200 {
		t.Fatalf("tag row: %d %v", code, tagOut)
	}
	if code, out := c.do("GET", "/api/v1/settings/request.max_uses?tag=env/prod", read, ""); code != 403 {
		t.Fatalf("a reader cannot probe overrides: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/settings/request.max_uses?tag=env/prod", admin, ""); code != 200 || out["value"] != float64(30) ||
		out["from"] != "tag" || out["target"] != "env/prod" || len(out["levels"].([]any)) != 3 { // default, system, tag
		t.Fatalf("resolved for a tag: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/settings", read, ""); code != 200 {
		t.Fatalf("list as reader: %d %v", code, out)
	} else if _, ok := out["overrides"]; ok {
		t.Fatalf("a reader must not see overrides: %v", out)
	}
	if code, out := c.do("GET", "/api/v1/settings", admin, ""); code != 200 || len(out["overrides"].([]any)) != 1 {
		t.Fatalf("overrides listed for admin: %d %v", code, out)
	}

	// reset, history, revert
	if code, out := c.do("DELETE", "/api/v1/settings/request.max_uses?scope=tag&target=env/prod&reason=finished-now", admin, ""); code != 200 || out["action"] != "reset" {
		t.Fatalf("reset: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/settings/history", read, ""); code != 403 {
		t.Fatalf("a reader cannot read history: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/settings/history", admin, ""); code != 200 || len(out["changes"].([]any)) != 3 {
		t.Fatalf("history: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/settings/history/1/revert", admin, `{"reason": "back to the default"}`); code != 200 || out["action"] != "revert" {
		t.Fatalf("revert: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/settings/request.max_uses", read, ""); code != 200 || out["from"] != "default" {
		t.Fatalf("after revert: %d %v", code, out)
	}
	if !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "claude-code set request.max_uses at system via API (version 1)") {
		t.Errorf("echo: %v", e.Bot.Texts)
	}
	if a := e.Store.AuditLog(3); a[0].Action != "settings_revert" || a[2].Action != "settings_set" {
		t.Errorf("audit: %+v", a)
	}
}
