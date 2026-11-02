package mcpserver

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestTokenTools(t *testing.T) {
	e := testutil.New(t)
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default()))
	defer srv.Close()

	cs := connect(t, srv.URL, e.Token(t, "codex", "status:read"))
	if res, text := call(t, cs, "whoami", nil); res.IsError || !strings.Contains(text, `"name":"codex"`) || !strings.Contains(text, `"via":"mcp"`) ||
		!strings.Contains(text, `"renewable":true`) || strings.Contains(text, `"hash"`) {
		t.Fatalf("whoami: %s", text)
	}
	res, text := call(t, cs, "renew_token", nil)
	if res.IsError || !strings.Contains(text, `"secret":"lbt_`) || !strings.Contains(text, `"old_secret_valid_until"`) {
		t.Fatalf("renew_token: %s", text)
	}
	if res, text := call(t, cs, "renew_token", nil); !res.IsError || !strings.Contains(text, "24 hours") {
		t.Errorf("renewing twice in a day: %s", text)
	}
	if a := e.Store.AuditLog(1); a[0].Action != "token_renew" || a[0].Via != "mcp" || a[0].Actor != "codex" {
		t.Errorf("audit: %+v", a[0])
	}

	probe, _, err := e.Store.CreateSubjectToken("probe-edge-1", "edge-1", []string{"probe:write"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	machine := connect(t, srv.URL, probe)
	if res, text := call(t, machine, "renew_token", nil); !res.IsError || !strings.Contains(text, "enroll -rotate") {
		t.Errorf("a machine's token renewing: %s", text)
	}
}
