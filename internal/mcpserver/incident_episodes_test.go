package mcpserver

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestListIncidentEpisodesIncludesOpenAlerts(t *testing.T) {
	e := testutil.New(t)
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default()))
	defer srv.Close()
	read := connect(t, srv.URL, e.Token(t, "read-episodes", "status:read"))
	in := map[string]any{"group": testutil.GroupID(), "severity": "critical"}
	if res, body := call(t, read, "list_incident_episodes", in); res.IsError ||
		!strings.Contains(body, "\"state\":\"open\"") || !strings.Contains(body, "\"opening_severity\":\"critical\"") ||
		!strings.Contains(body, "\"wake\":\"unknown\"") || !strings.Contains(body, "\"retained_from\":") {
		t.Fatalf("open episode accounting: %s", body)
	}
}
