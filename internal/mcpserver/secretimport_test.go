package mcpserver

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestImportSecretsToolIsGone(t *testing.T) {
	e := testutil.New(t)
	vault, err := secrets.Open(context.Background(),
		secrets.SealConfig{Mode: secrets.SealFile, Dir: filepath.Join(t.TempDir(), "secrets")},
		secrets.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN, CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(), WithVault(vault)))
	defer srv.Close()
	cs := connect(t, srv.URL, e.Token(t, "claude-code", "status:read", "secrets:admin"))
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "import_secrets" {
			t.Fatal("import is the console and the command line, not MCP")
		}
	}
}
