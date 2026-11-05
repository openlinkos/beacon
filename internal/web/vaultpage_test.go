package web

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets"
)

func TestConsoleUnsealDoesNotInit(t *testing.T) {
	restore := secrets.UseCheapPassphraseForTest()
	t.Cleanup(restore)
	e := newEnv(t, i18n.EN)
	svc, err := secrets.Open(context.Background(), secrets.SealConfig{Mode: secrets.SealPassphrase, Dir: filepath.Join(t.TempDir(), "secrets")},
		secrets.Deps{Store: e.st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Vault = svc
	e.mux = http.NewServeMux()
	e.srv.Register(e.mux)
	alice := e.owner()
	page := alice.get("/console/secrets")
	if page.code != http.StatusOK {
		t.Fatalf("the secrets page: %d %s", page.code, page.body)
	}
	if strings.Contains(page.body, `action="` ) && strings.Contains(page.body, "/secrets/unseal") {
		t.Fatal("the unseal form must not show before the vault is initialised")
	}
	if !strings.Contains(page.body, "/secrets/init") {
		t.Fatal("the initialise form shows while the vault has no passphrase")
	}
	if a := alice.post("/console/secrets/unseal", url.Values{"passphrase": {"first-passphrase-here"}}); a.code != http.StatusSeeOther {
		t.Fatalf("unseal before init: %d %s", a.code, a.body)
	}
	if !svc.Sealed() || svc.Initialised() {
		t.Fatal("posting unseal must not take the first passphrase")
	}
	if a := alice.post("/console/secrets/init", url.Values{"passphrase": {"first-passphrase-here"}}); a.code != http.StatusSeeOther {
		t.Fatalf("init: %d %s", a.code, a.body)
	}
	if svc.Sealed() || !svc.Initialised() {
		t.Fatal("init takes the first passphrase and unseals")
	}
}
