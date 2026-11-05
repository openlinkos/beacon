package api

import (
	"testing"
)

func TestVaultImportRouteIsGone(t *testing.T) {
	e, c := setup(t)
	owner := e.Token(t, "owner", "secrets:admin")
	if code, _ := c.do("POST", "/api/v1/vault/import", owner, `{"batch":{"source":"chrome","collections":[]}}`); code != 404 {
		t.Fatalf("import is the console and the command line, not REST: %d", code)
	}
}
