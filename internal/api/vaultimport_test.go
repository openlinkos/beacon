package api

import (
	"testing"

	"github.com/openlinkos/beacon/internal/secrets"
)

// The import's route on the REST API: the door, as the vault's other doors are tested — the
// operation itself is the service's own test. A credential that is not a person gets the person
// refusal (an agent the agent's), and the scope comes first.
func TestVaultImportRouteDoors(t *testing.T) {
	e, c, _ := setupVault(t)
	body := `{"batch": {"source": "chrome", "collections": [{"name": "Chrome", "items": []}]}}`
	if code, out := c.do("POST", "/api/v1/vault/import", e.Token(t, "reader", "status:read"), body); code != 403 || out["code"] != "auth.scope" {
		t.Fatalf("importing needs secrets:admin: %d %v", code, out)
	}
	owner := e.Token(t, "admin", "secrets:admin")
	if code, out := c.do("POST", "/api/v1/vault/import", owner, body); code != 403 || out["code"] != "secrets.person" {
		t.Fatalf("a token is not a person: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/vault/import", owner, `{"batch": {}}`); code != 403 || out["code"] != "secrets.person" {
		t.Fatalf("the person check comes before the batch's shape: %d %v", code, out)
	}
	// a dry-run's own route shape is refused as any other unknown one
	if code, _ := c.do("POST", "/api/v1/vault/import/dry-run", owner, body); code != 404 {
		t.Fatalf("the dry-run is the same route with dry_run set: %d", code)
	}
	_ = secrets.LevelAuto // the levels the options take are the vault's own
}
