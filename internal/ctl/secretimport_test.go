package ctl

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
)

// The import's route on the socket: the batch and its options go in, the report comes back, and
// the vault's errors answer as their own statuses. The stub stands in for the vault, as the
// connector test's fake stands in for the SSH channel.
type importStub struct {
	who     secrets.Actor
	batch   importer.Batch
	options secrets.ImportOptions
	report  secrets.ImportReport
	err     error
}

func (s *importStub) ActorForUser(name string) (secrets.Actor, bool) {
	if name == "alice" {
		return secrets.Actor{Name: "alice", Via: "cli", Role: "owner"}, true
	}
	return secrets.Actor{}, false
}

func (s *importStub) Import(who secrets.Actor, batch importer.Batch, opts secrets.ImportOptions) (secrets.ImportReport, error) {
	s.who, s.batch, s.options = who, batch, opts
	return s.report, s.err
}

func newImportClient(t *testing.T, v ImportVault) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/secrets/import", importRoute(v, fail))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
}

func TestVaultImportRoute(t *testing.T) {
	stub := &importStub{report: secrets.ImportReport{Source: "chrome", Created: 2,
		Collections: []secrets.CollectionReport{{Name: "Chrome", Items: 2, Level: "person"}}}}
	c := newImportClient(t, stub)
	batch := importer.Batch{Source: importer.SourceChrome, Collections: []importer.CollectionImport{
		{Name: "Chrome", LevelKey: "chrome", Items: []importer.ItemImport{{
			Title: "Example", Category: "login",
			Fields: []importer.FieldImport{{Name: "username", Type: "string", Purpose: "username", Value: "user@example.com"}},
		}}}}}
	report, err := c.VaultImport(context.Background(), "alice", batch, secrets.ImportOptions{OnConflict: "new_version"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Source != "chrome" || report.Created != 2 || len(report.Collections) != 1 {
		t.Fatalf("the report comes back: %+v", report)
	}
	if stub.who.Name != "alice" || stub.who.Via != "cli" || stub.who.Role != "owner" {
		t.Fatalf("the import runs as the named account: %+v", stub.who)
	}
	if stub.batch.Source != "chrome" || len(stub.batch.Collections[0].Items) != 1 {
		t.Fatalf("the batch travels whole: %+v", stub.batch)
	}
	if stub.options.OnConflict != "new_version" {
		t.Fatalf("the options travel: %+v", stub.options)
	}
	// an account that is not there is refused before the vault hears of it
	if _, err := c.VaultImport(context.Background(), "nobody", batch, secrets.ImportOptions{}); err == nil || !strings.Contains(err.Error(), "name the account") {
		t.Fatalf("a missing account: %v", err)
	}
}

func TestVaultImportRouteMapsTheVaultsErrors(t *testing.T) {
	stub := &importStub{err: secrets.ErrSealed}
	c := newImportClient(t, stub)
	batch := importer.Batch{Source: importer.SourceCSV, Collections: []importer.CollectionImport{{Name: "Sheet"}}}
	if _, err := c.VaultImport(context.Background(), "alice", batch, secrets.ImportOptions{}); err == nil || !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("the sealed vault's error crosses the socket: %v", err)
	}
}
