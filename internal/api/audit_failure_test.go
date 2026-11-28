package api

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestGrantsNeedTheirRecord checks that, while audit entries cannot be written, a change that gives
// access (an agent, its token, a key, a renewal) is not made and is refused, every failure is reported,
// and taking access away still works.
func TestGrantsNeedTheirRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Store = st })
	e.Store = st
	c := serveTestAPI(t, e)
	agent := people(t, e)
	admin := e.Token(t, "owner-script", "status:read", "sessions:read", "accounts:write")
	renewing := e.Token(t, "renewing", "status:read")
	var mu sync.Mutex
	var failed []string
	st.OnAuditFailure(func(e store.AuditEntry, _ error) { mu.Lock(); failed = append(failed, e.Action); mu.Unlock() })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}

	code, out := c.do("POST", "/api/v1/agents", admin, `{"name": "unrecorded", "owner": "bob", "preset": "readonly", "expires": "720h"}`)
	if code != 503 || out["code"] != "audit.not_recorded" {
		t.Errorf("an agent made without its record: %d %v", code, out)
	}
	if _, ok := e.Store.Agent("unrecorded"); ok {
		t.Error("the agent stayed")
	}
	tokens := len(e.Store.AgentTokens("codex"))
	if _, err := e.Bridge.NewAgentToken(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, "codex", time.Hour); err == nil {
		t.Error("a token issued without its record")
	}
	if n := len(e.Store.AgentTokens("codex")); n != tokens {
		t.Errorf("the token stayed: %d tokens, %d before", n, tokens)
	}
	code, out = c.do("POST", "/api/v1/ssh-keys", agent, `{"key": "`+pubKey(t)+`"}`)
	if code != 503 || out["code"] != "audit.not_recorded" {
		t.Errorf("a key added without its record: %d %v", code, out)
	}
	if a, _ := e.Store.Agent("codex"); len(a.SSHKeys) != 0 {
		t.Errorf("the key stayed: %+v", a.SSHKeys)
	}
	code, out = c.do("POST", "/api/v1/token/renew", renewing, "")
	if code != 503 || out["code"] != "audit.not_recorded" {
		t.Errorf("a renewal without its record: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/v1/token", renewing, ""); code != 200 {
		t.Errorf("the token lost its secret: %d", code)
	}

	// taking access away does not wait for the record
	if err := e.Bridge.DeleteAgent(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, "codex"); err != nil {
		t.Errorf("deleting an agent: %v", err)
	}
	if _, ok := e.Store.Agent("codex"); ok {
		t.Error("the agent was not deleted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failed) < 4 {
		t.Errorf("failures reported: %v", failed)
	}
}
