package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestMachineWriteNeedsItsRecord: while audit entries cannot be written, a machine's connector key is not
// set, and the answer says so (audit.not_recorded, 503).
func TestMachineWriteNeedsItsRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Store = st })
	e.Store = st
	gate, err := sshgate.New(sshgate.Deps{Store: st, HostKey: lockSigner(t), CA: lockSigner(t), Dir: filepath.Join(t.TempDir(), "rec"),
		Lang: i18n.EN, Bot: e.Bot, Log: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutMachine(store.Machine{Name: "nat-1", Address: store.ConnectorAddress, User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Server{Bridge: e.Bridge, SSH: gate, Auth: &Auth{Store: st}, Log: slog.Default()}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := client{t, srv}
	writer := e.Token(t, "machines-writer", "machines:write")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(lockSigner(t).PublicKey())))
	code, out := c.do("PUT", "/api/v1/machines/nat-1/connector", writer, `{"key": "`+key+`"}`)
	if code != http.StatusServiceUnavailable || out["code"] != "audit.not_recorded" {
		t.Errorf("a connector key set without its record: %d %v", code, out)
	}
	if m, _ := st.Machine("nat-1"); m.ConnectorKey != "" {
		t.Errorf("the key was set: %+v", m)
	}
}
