package api

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

func TestTokenRoutes(t *testing.T) {
	e, c := setup(t)
	secret := e.Token(t, "claude-code", "status:read", "alerts:write")
	code, out := c.do("GET", "/api/v1/token", secret, "")
	if code != 200 || out["name"] != "claude-code" || out["lifetime"] != "1h0m0s" || out["renewable"] != true || out["via"] != "api" || out["expires"] == nil {
		t.Fatalf("whoami: %d %v", code, out)
	}
	if scopes, _ := out["scopes"].([]any); len(scopes) != 2 || out["hash"] != nil || out["secret"] != nil {
		t.Errorf("whoami shows the scopes and never the secret: %v", out)
	}
	if code, out := c.do("GET", "/api/v1/token", "lbt_wrong", ""); code != 401 || out["code"] != "auth.invalid" {
		t.Errorf("an unknown token has no identity: %d %v", code, out)
	}

	code, out = c.do("POST", "/api/v1/token/renew", secret, "")
	renewed, _ := out["secret"].(string)
	tok, _ := out["token"].(map[string]any)
	if code != 200 || !strings.HasPrefix(renewed, store.TokenPrefix) || renewed == secret || tok["name"] != "claude-code" || out["old_secret_valid_until"] == nil {
		t.Fatalf("renew: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/token", renewed, ""); code != 200 || out["renewed"] == nil {
		t.Errorf("the new secret works and knows its renewal: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/alerts", secret, ""); code != 200 {
		t.Errorf("the old secret works during the grace period: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/token/renew", renewed, ""); code != 429 || out["code"] != "token.too_soon" {
		t.Errorf("renewing twice in a day: %d %v", code, out)
	}
	if a := e.Store.AuditLog(1); a[0].Action != "token_renew" || a[0].Actor != "claude-code" || a[0].Via != "api" || strings.Contains(a[0].Detail, renewed) {
		t.Errorf("audit: %+v", a[0])
	}
	expires := time.Now().Add(time.Hour).Format(time.DateOnly)
	if !slices.ContainsFunc(e.Bot.Texts, func(s string) bool { return s == "Token claude-code renewed itself; expires "+expires }) {
		t.Errorf("echo: %v", e.Bot.Texts)
	}

	probe, _, err := e.Store.CreateSubjectToken("probe-edge-1", "edge-1", []string{"probe:write"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	if code, out := c.do("GET", "/api/v1/token", probe, ""); code != 200 || out["renewable"] != false || out["renewable_at"] != nil {
		t.Errorf("a machine's token is not renewable: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/token/renew", probe, ""); code != 403 || out["code"] != "token.not_renewable" {
		t.Errorf("a machine's token renewing: %d %v", code, out)
	}
}
