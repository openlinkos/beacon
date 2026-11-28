package ctl

import (
	"context"
	"database/sql"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
)

// TestTokensNeedTheirRecord checks that, while audit entries cannot be written, the command line makes
// no token and enrols no machine.
func TestTokensNeedTheirRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(st, nil, nil))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()
	if _, err := c.CreateToken(ctx, TokenRequest{Name: "unrecorded", Scopes: []string{"status:read"}, TTL: "1h"}); err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Errorf("a token made without its record: %v", err)
	}
	if _, err := c.Enroll(ctx, EnrollRequest{Kind: "host", Subject: "worker-1"}); err == nil {
		t.Error("a machine enrolled without its record")
	}
	if n := len(st.Tokens()); n != 0 {
		t.Errorf("%d tokens stayed", n)
	}
}
