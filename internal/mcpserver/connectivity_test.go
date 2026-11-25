package mcpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestMachineSSHCheckRESTAndMCP(t *testing.T) {
	e := testutil.New(t)
	gate, err := sshgate.New(sshgate.Deps{Store: e.Store, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inside", "outside"} {
		if err := e.Store.PutMachine(store.Machine{Name: name, Address: store.ConnectorAddress, User: "beacon"}); err != nil {
			t.Fatal(err)
		}
	}
	secret := e.Token(t, "checks", "machines:ssh")
	if _, err := e.Store.UpdateToken("checks", func(tok *store.Token) error {
		tok.Machines = []string{"inside"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	auth := &api.Auth{Store: e.Store}
	mux := http.NewServeMux()
	(&api.Server{Bridge: e.Bridge, SSH: gate, Auth: auth, Log: slog.Default()}).Register(mux)
	mux.Handle("/mcp", Handler(e.Bridge, SSH{Gate: gate}, auth, slog.Default()))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	post := func(bearer, machine string) (int, string) {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/machines/"+machine+"/check", nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(body)
	}
	if code, _ := post("", "inside"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous REST returned %d", code)
	}
	if code, body := post(secret, "outside"); code != http.StatusForbidden || !strings.Contains(body, "machine.forbidden") {
		t.Fatalf("REST outside the machine ceiling: %d %s", code, body)
	}
	code, body := post(secret, "inside")
	if code != http.StatusOK {
		t.Fatalf("authorised REST returned %d %s", code, body)
	}
	var rest sshgate.SSHCheck
	if err := json.Unmarshal([]byte(body), &rest); err != nil || rest.Route != "connector" || rest.Status != sshgate.SSHCheckDisconnected {
		t.Fatalf("REST connector observation = %s, %v", body, err)
	}
	connection := connect(t, srv.URL+"/mcp", secret)
	if response, _ := call(t, connection, "check_machine_ssh", map[string]any{"machine": "outside"}); response == nil || !response.IsError {
		t.Fatal("MCP escaped the machine ceiling")
	}
	response, body := call(t, connection, "check_machine_ssh", map[string]any{"machine": "inside"})
	var mcp sshgate.SSHCheck
	if response == nil || response.IsError {
		t.Fatalf("MCP connector observation = %s", body)
	}
	data, err := json.Marshal(response.StructuredContent)
	if err != nil || json.Unmarshal(data, &mcp) != nil || mcp.Machine != rest.Machine || mcp.Status != rest.Status {
		t.Fatalf("MCP connector observation = %s", body)
	}
	audit, _, err := e.Store.AuditQuery(store.AuditFilter{Action: "machine_ssh_check"})
	if err != nil || len(audit) != 4 || audit[0].Via != "mcp" || audit[0].Detail != mcp.ID+" connector_disconnected" ||
		audit[2].Via != "api" || audit[2].Detail != rest.ID+" connector_disconnected" {
		t.Fatalf("only the permitted checks should be audited: %+v, %v", audit, err)
	}
}
