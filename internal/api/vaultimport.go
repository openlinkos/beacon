package api

import (
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
	"github.com/openlinkos/beacon/internal/store"
)

// The import's route on the REST API: the same operation the console's import wizard, the MCP tool
// and `beacon secret import` offer, through the same service. The body is the parsed batch (the
// console and the command line parse the file where they run; a dry-run's batch carries no values
// at all) with its options; the answer is the report, which never carries a value. Importing is
// the vault's own door: a person with secrets:admin, refused for agents.

func vaultImportRoutes() []string {
	return []string{"POST /api/v1/vault/import"}
}

func init() { Routes = append(Routes, vaultImportRoutes()...) }

// registerVaultImport adds the import's route; nil vault leaves it out (registerVault does the
// rest of the vault).
func (s *Server) registerVaultImport(mux *http.ServeMux) {
	if s.Vault == nil {
		return
	}
	mux.Handle("POST /api/v1/vault/import", s.withPrincipal("secrets:admin", s.postImport))
}

// importIn is the route's body.
type importIn struct {
	Batch   importer.Batch        `json:"batch"`
	Options secrets.ImportOptions `json:"options"`
}

func (s *Server) postImport(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in importIn
	if !readBody(w, r, &in, 256<<20) {
		return
	}
	report, err := s.Vault.Import(who, in.Batch, in.Options)
	if err != nil {
		if errors.Is(err, secrets.ErrAttachment) {
			writeError(w, http.StatusRequestEntityTooLarge, "secrets.attachment_too_large", err.Error())
			return
		}
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
