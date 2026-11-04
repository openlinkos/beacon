package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/testutil"
)

func setupVault(t *testing.T) (*testutil.Env, client, *secrets.Service) {
	e := testutil.New(t)
	svc, err := secrets.Open(context.Background(), secrets.SealConfig{Mode: secrets.SealFile, Dir: filepath.Join(t.TempDir(), "secrets")},
		secrets.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN, CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Vault: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return e, client{t, srv}, svc
}

// The vault's routes: the state tells the seal mode and the rows that failed their signature, and
// the vault's own doors answer a token that is not a person with the plain refusal — the grants
// agents will ask through arrive with a later package.
func TestVaultRoutesState(t *testing.T) {
	e, c, svc := setupVault(t)
	reader := e.Token(t, "reader", "status:read")
	_ = e
	if _, err := svc.CreateCollection(secrets.Actor{Name: "owner", Via: "test", Role: "owner"}, "Infra", "", secrets.LevelReview); err != nil {
		t.Fatal(err)
	}
	if code, out := c.do("GET", "/api/v1/vault", reader, ""); code != 200 || out["mode"] != "file" || out["sealed"] != false {
		t.Fatalf("the state route answers the mode: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/vault/collections/Infra/items", reader, `{"title": "Example"}`); code != 403 || out["code"] != "secrets.person" {
		t.Fatalf("a token that is not a person gets the person refusal: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/vault/collections", reader, `{"name": "Other"}`); code != 403 || out["code"] != "auth.scope" {
		t.Fatalf("making a collection needs secrets:admin: %d %v", code, out)
	}
	// the seal answers with its stable code and the HTTP status that says to try later
	if err := svc.Unseal("nothing"); err == nil || !strings.Contains(err.Error(), "only the passphrase mode") {
		t.Fatalf("unseal outside the passphrase mode is refused, got %v", err)
	}
}
