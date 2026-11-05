package mcpserver

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/testutil"
)

// The import's tool: on the list, and — like every vault tool — answering a token's caller with
// the plain refusal, for the vault is people's until the grants arrive.
func TestImportSecretsTool(t *testing.T) {
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
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "import_secrets" {
			found = true
		}
	}
	if !found {
		t.Fatal("import_secrets is on the list")
	}
	batch := map[string]any{
		"batch": map[string]any{
			"source": "chrome",
			"collections": []any{map[string]any{
				"name": "Chrome", "items": []any{map[string]any{
					"title": "Example", "fields": []any{map[string]any{
						"name": "username", "type": "string", "purpose": "username", "value": "user@example.com"}},
				}},
			}},
		},
	}
	res, text := call(t, cs, "import_secrets", batch)
	if !res.IsError || !strings.Contains(text, "lacks the") {
		t.Fatalf("a token's caller is refused, as every vault tool refuses what is not a person: %s", text)
	}
	res, _ = call(t, cs, "import_secrets", map[string]any{"batch": map[string]any{"source": "chrome", "collections": []any{}}})
	if !res.IsError {
		t.Fatal("the refusal comes first: nothing of the vault answers a token's caller")
	}
}
