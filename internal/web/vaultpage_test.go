package web

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets"
)

func TestVaultPageHidesCollectionsWithoutDescribe(t *testing.T) {
	e := newEnv(t, i18n.EN)
	alice := e.owner()
	bob := e.invite(alice, "bob", "viewer")
	svc, err := secrets.Open(context.Background(), secrets.SealConfig{Mode: secrets.SealFile, Dir: filepath.Join(t.TempDir(), "secrets")},
		secrets.Deps{Store: e.st, Lang: i18n.EN, CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Vault = svc
	e.mux = http.NewServeMux()
	e.srv.Register(e.mux)
	if _, err := svc.CreateCollection(secrets.Actor{Name: "alice", Via: "test", Role: "owner"}, "Infra", "", secrets.LevelReview, nil); err != nil {
		t.Fatal(err)
	}
	if a := alice.get("/console/secrets"); a.code != http.StatusOK || !strings.Contains(a.body, "Infra") {
		t.Fatalf("the owner sees the collection: %d", a.code)
	}
	if a := bob.get("/console/secrets"); a.code != http.StatusOK || strings.Contains(a.body, "Infra") {
		t.Fatalf("without describe the collection is hidden: %d %s", a.code, a.body)
	}
}
