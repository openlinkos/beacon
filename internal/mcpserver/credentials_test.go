package mcpserver

import (
	"bytes"
	"context"
	"crypto/x509"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestMCPTeamCredentials(t *testing.T) {
	e := testutil.New(t)
	code, _, err := e.Store.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	account, err := e.Store.Join(code, "alice", "Alice", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := e.Store.AccountPrincipal("alice")
	sessionID, _, err := e.Store.CreateAccountSession(account, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	owner = owner.InConsoleSession(sessionID)
	if _, err := e.Store.CreateAgent(store.Agent{Name: "duty-agent", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", secrets.UseScope, secrets.ManageScope}, CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	token, _, err := e.Store.CreateAgentToken("duty-agent", time.Hour, "alice")
	if err != nil {
		t.Fatal(err)
	}
	const value = "fixture-credential-api"
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+value {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(value))
	}))
	t.Cleanup(target.Close)
	uri, _ := url.Parse(target.URL)
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	vault, err := secrets.Open(bytes.Repeat([]byte{0x42}, 32), secrets.Deps{
		Store: e.Store, AnchorDir: t.TempDir(), AllowedHosts: []string{uri.Host}, RootCAs: roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := vault.Create(ctx, owner, "console", secrets.NewCredential{Name: "Team API", Endpoint: target.URL, Value: value})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithTeamCredentials(vault, true)))
	t.Cleanup(srv.Close)
	session := connect(t, srv.URL, token)
	in := map[string]any{"id": c.ID}
	if answer, _ := call(t, session, "use_team_credential", in); !answer.IsError {
		t.Fatal("an ungranted agent could use the credential")
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "rotate_team_credential" || tool.Name == "create_team_credential" || tool.Name == "grant_team_credential" {
			t.Fatalf("the agent surface offers a custody tool %s", tool.Name)
		}
	}
	c, err = vault.SetGrant(ctx, owner, "console", c.ID, c.Version, "duty-agent", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if answer, text := call(t, session, "list_team_credentials", map[string]any{}); answer.IsError || !strings.Contains(text, c.ID) ||
		strings.Contains(text, value) || strings.Contains(text, target.URL) {
		t.Fatalf("agent list did not respect the metadata boundary: error %v", answer.IsError)
	}
	if answer, text := call(t, session, "use_team_credential", in); answer.IsError || !strings.Contains(text, "200") || strings.Contains(text, value) {
		t.Fatalf("agent use did not return a status-only result: error %v", answer.IsError)
	}
	if _, err := vault.Revoke(ctx, owner, "console", c.ID, c.Version, "duty-agent"); err != nil {
		t.Fatal(err)
	}
	if answer, _ := call(t, session, "use_team_credential", in); !answer.IsError {
		t.Fatal("the same MCP session could use the revoked grant")
	}
}
