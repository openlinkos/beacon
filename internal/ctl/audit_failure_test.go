package ctl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

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

// TestTokenChangesNeedTheirRecord checks that, while audit entries cannot be written, the command line
// rotates no machine's token (the old one keeps working), adds no key to a token and widens no token's
// machines, and still narrows them.
func TestTokenChangesNeedTheirRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutMachine(store.Machine{Name: "box", Address: "box.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(st, nil, nil))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()
	enrolled, err := c.Enroll(ctx, EnrollRequest{Kind: "host", Subject: "worker-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateToken(ctx, TokenRequest{Name: "script", Scopes: []string{"machines:ssh"}, TTL: "1h", Machines: []string{"box"}}); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enroll(ctx, EnrollRequest{Kind: "host", Subject: "worker-1", Rotate: true}); err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Errorf("a token rotated without its record: %v", err)
	}
	if p, ok := st.Authenticate(enrolled.Secret); !ok || p.Token != "host-worker-1" {
		t.Error("a rotation that was not recorded left the machine without its token")
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewPublicKey(pub)
	if _, err := c.AddSSHKey(ctx, "script", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))); err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Errorf("a key added without its record: %v", err)
	}
	if _, err := c.SetTokenMachines(ctx, "script", []string{"all"}); err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Errorf("a token widened without its record: %v", err)
	}
	for _, tok := range st.Tokens() {
		if tok.Name == "script" && (len(tok.SSHKeys) != 0 || tok.IsAll()) {
			t.Errorf("the token changed without its record: %+v", tok)
		}
	}
	// narrowing goes ahead
	if _, err := c.SetTokenMachines(ctx, "script", []string{"none"}); err != nil {
		t.Errorf("narrowing a token: %v", err)
	}

	// once the audit log can be written, a rotation replaces the token at once
	if _, err := db.Exec("DROP TRIGGER reject_audit"); err != nil {
		t.Fatal(err)
	}
	rotated, err := c.Enroll(ctx, EnrollRequest{Kind: "host", Subject: "worker-1", Rotate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Authenticate(enrolled.Secret); ok {
		t.Error("the old token outlives its rotation")
	}
	if _, ok := st.Authenticate(rotated.Secret); !ok || !rotated.Token.Expires.After(time.Now()) {
		t.Error("the new token does not work")
	}
	found := false
	for _, e := range st.AuditLog(20) {
		found = found || e.Action == "enroll_rotate" && e.Target == "host-worker-1"
	}
	if !found {
		t.Errorf("audit: %+v", st.AuditLog(20))
	}
}
