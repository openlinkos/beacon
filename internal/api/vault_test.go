package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

func TestVaultCollectionsHiddenWithoutDescribe(t *testing.T) {
	e, c, svc := setupVault(t)
	agent := people(t, e)
	if _, err := svc.CreateCollection(secrets.Actor{Name: "alice", Via: "test", Role: "owner"}, "Infra", "", secrets.LevelReview); err != nil {
		t.Fatal(err)
	}
	if code, out := c.do("GET", "/api/v1/vault/collections", agent, ""); code != 403 || out["code"] != "secrets.agent" {
		t.Fatalf("an agent listing collections: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/vault/tags", agent, ""); code != 403 || out["code"] != "secrets.agent" {
		t.Fatalf("an agent listing tags: %d %v", code, out)
	}
	srv := &Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Vault: svc}
	bob, ok := e.Store.AccountPrincipal("bob")
	if !ok {
		t.Fatal("bob")
	}
	rec := httptest.NewRecorder()
	srv.listCollections(rec, httptest.NewRequest(http.MethodGet, "/api/v1/vault/collections", nil), bob)
	var got struct {
		Collections []secrets.Collection `json:"collections"`
	}
	if rec.Code != 200 {
		t.Fatalf("bob lists collections: %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Collections) != 0 {
		t.Fatalf("without describe bob sees no collections, got %v", got.Collections)
	}
	alice, _ := e.Store.AccountPrincipal("alice")
	rec = httptest.NewRecorder()
	srv.listCollections(rec, httptest.NewRequest(http.MethodGet, "/api/v1/vault/collections", nil), alice)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Collections) != 1 || got.Collections[0].Name != "Infra" {
		t.Fatalf("the owner sees the collection, got %v", got.Collections)
	}
}

func TestVaultUnsealNeedsAdminAndIsRateLimited(t *testing.T) {
	restore := secrets.UseCheapPassphraseForTest()
	t.Cleanup(restore)
	e := testutil.New(t)
	svc, err := secrets.Open(context.Background(), secrets.SealConfig{Mode: secrets.SealPassphrase, Dir: filepath.Join(t.TempDir(), "secrets")},
		secrets.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN, CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Vault: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := client{t, srv}
	reader := e.Token(t, "reader", "status:read")
	if code, out := c.do("POST", "/api/v1/vault/unseal", reader, `{"passphrase":"x"}`); code != 403 || out["code"] != "auth.scope" {
		t.Fatalf("unseal without secrets:admin: %d %v", code, out)
	}
	if err := svc.InitPassphrase("correct-horse-battery-staple-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Seal(); err != nil {
		t.Fatal(err)
	}
	admin := e.Token(t, "admin", "secrets:admin")
	for range 20 {
		c.do("POST", "/api/v1/vault/unseal", admin, `{"passphrase":"wrong-passphrase-here"}`)
	}
	if code, out := c.do("POST", "/api/v1/vault/unseal", admin, `{"passphrase":"wrong-passphrase-here"}`); code != 429 || out["code"] != "auth.too_many_failures" {
		t.Fatalf("after many wrong passphrases: %d %v", code, out)
	}
}

func TestRevealWrongPasswordIsRateLimitedHTTP(t *testing.T) {
	e := testutil.New(t)
	svc, err := secrets.Open(context.Background(), secrets.SealConfig{Mode: secrets.SealFile, Dir: filepath.Join(t.TempDir(), "secrets")},
		secrets.Deps{Store: e.Store, Bot: e.Bot, Lang: i18n.EN, CheckPassword: func(string, string) error { return errors.New("no") }})
	if err != nil {
		t.Fatal(err)
	}
	_ = people(t, e)
	alice, ok := e.Store.AccountPrincipal("alice")
	if !ok {
		t.Fatal("alice")
	}
	who := secrets.Actor{Name: "alice", Via: "api", Role: "owner"}
	col, err := svc.CreateCollection(who, "Infra", "", secrets.LevelReview)
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.SetItem(who, secrets.ItemIn{Collection: col.Name, Title: "Example",
		Fields: []secrets.FieldIn{{Name: "password", Type: secrets.FieldConcealed, Value: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Bridge: e.Bridge, Auth: &Auth{Store: e.Store}, Log: slog.Default(), Vault: svc}
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/vault/items/"+item.ID+"/reveal", strings.NewReader(`{"password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", item.ID)
		srv.revealItem(rec, req, alice)
		return rec
	}
	for i := 0; i < 9; i++ {
		rec := post()
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("fail %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := post()
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusTooManyRequests || out["code"] != "secrets.too_many_failures" {
		t.Fatalf("the tenth wrong password: %d %v", rec.Code, out)
	}
	if wait, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || wait < 1 {
		t.Fatalf("Retry-After: %q", rec.Header().Get("Retry-After"))
	}
}
