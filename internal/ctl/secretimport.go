package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
)

// The import's routes on the admin socket, beside the vault's own (`beacon secret import …`). The
// command line parses the export where it runs and sends the batch here; a dry-run batch has no
// values in it at all (the importer strips them), and the report that comes back never carries one.

// ImportVault is what the import needs of the vault.
type ImportVault interface {
	ActorForUser(name string) (secrets.Actor, bool)
	Import(who secrets.Actor, batch importer.Batch, opts secrets.ImportOptions) (secrets.ImportReport, error)
}

// importRequest is the body: the parsed batch and how it runs.
type importRequest struct {
	Batch   importer.Batch        `json:"batch"`
	Options secrets.ImportOptions `json:"options"`
}

// importRoute serves one import. The body is the batch (a dry-run's carries no values), so it is
// allowed what an export weighs; the socket is the data directory's own.
func importRoute(v ImportVault, fail func(w http.ResponseWriter, code int, err error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		who, ok := v.ActorForUser(r.URL.Query().Get("user"))
		if !ok {
			fail(w, http.StatusForbidden, errors.New("name the account whose hands these are: -user NAME (it exists, as `beacon user ls` lists)"))
			return
		}
		var in importRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		report, err := v.Import(who, in.Batch, in.Options)
		if err != nil {
			code := http.StatusInternalServerError
			switch {
			case errors.Is(err, secrets.ErrSealed):
				code = http.StatusServiceUnavailable
			case errors.Is(err, secrets.ErrAgent), errors.Is(err, secrets.ErrForbidden):
				code = http.StatusForbidden
			case errors.Is(err, secrets.ErrInvalid), errors.Is(err, secrets.ErrBadInput):
				code = http.StatusBadRequest
			case errors.Is(err, secrets.ErrNotFound):
				code = http.StatusNotFound
			case errors.Is(err, secrets.ErrExists), errors.Is(err, secrets.ErrConflict):
				code = http.StatusConflict
			case errors.Is(err, secrets.ErrAttachment):
				code = http.StatusRequestEntityTooLarge
			}
			fail(w, code, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	}
}

// VaultImport runs one import over the socket, as a named account.
func (c *Client) VaultImport(ctx context.Context, user string, batch importer.Batch, opts secrets.ImportOptions) (secrets.ImportReport, error) {
	var out secrets.ImportReport
	return out, c.do(ctx, "POST", "/v1/secrets/import?user="+user, importRequest{Batch: batch, Options: opts}, &out)
}
