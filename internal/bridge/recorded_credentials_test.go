package bridge_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestCredentialsAreRecordedOrRefused: while audit entries cannot be written, nobody joins with an
// invitation, no token renews itself, and no agent, agent token or SSH key (a person's, a token's or an
// agent's) is made; each is refused as unrecorded and leaves nothing behind. Once the log can be written
// again, the same invitation and the old token secret still work.
func TestCredentialsAreRecordedOrRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Store = st })
	e.Store = st
	ctx := context.Background()
	cli := accounts(t, e)
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "codex", Owner: "bob", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	inv, err := e.Bridge.Invite(ctx, cli, "viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	renewing, _, err := e.Store.CreateToken("renewing", []string{"status:read"}, 30*24*time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	renewer, _ := e.Store.Authenticate(renewing)
	keyed := tokenActor(t, e, "keyed", store.NamedAccess(), "machines:ssh")
	ap, _ := e.Store.AccountPrincipal("alice")
	alice := bridge.ActorOf(ap, "console")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}

	key := sshKeyLine(t)
	for what, err := range map[string]error{
		"joining with an invitation": func() error {
			_, err := e.Bridge.Join(ctx, linkCode(t, inv), "carol", "", "correct horse battery")
			return err
		}(),
		"renewing a token": func() error { _, err := e.Bridge.RenewToken(ctx, renewer, "api"); return err }(),
		"a person's key":   func() error { _, err := e.Bridge.AddSSHKey(ctx, alice, "", key, ""); return err }(),
		"a token's key":    func() error { _, err := e.Bridge.AddSSHKey(ctx, keyed, "", key, ""); return err }(),
		"an agent's key":   func() error { _, err := e.Bridge.AddSSHKey(ctx, cli, "codex", key, ""); return err }(),
		"an agent with a key": func() error {
			_, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "gemini", Owner: "bob", Preset: "readonly", SSHKeys: []string{key}})
			return err
		}(),
		"an agent's token": func() error { _, err := e.Bridge.NewAgentToken(ctx, cli, "codex", 24*time.Hour); return err }(),
	} {
		if !errors.Is(err, bridge.ErrUnrecorded) {
			t.Errorf("%s without its record: %v", what, err)
		}
	}
	if _, ok := e.Store.Account("carol"); ok {
		t.Error("an account was made without its record")
	}
	if _, ok := e.Store.VerifyToken(renewing); !ok {
		t.Error("the token lost its secret to a renewal without a record")
	}
	if fp, _ := store.ParseSSHKey(key); e.Store.KeyHolder(fp.Fingerprint) != "" {
		t.Errorf("a key was added without its record: %s", e.Store.KeyHolder(fp.Fingerprint))
	}
	if _, ok := e.Store.Agent("gemini"); ok {
		t.Error("an agent was made without its record")
	}
	for _, tok := range e.Store.Tokens() {
		if tok.Agent == "codex" {
			t.Errorf("an agent's token was issued without its record: %s", tok.Name)
		}
		if tok.Name == "renewing" && (!tok.Renewed.IsZero() || tok.OldHash != "") {
			t.Errorf("the token was renewed without a record: %+v", tok)
		}
	}

	// once the log can be written, the link and the secret still work
	if _, err := db.Exec("DROP TRIGGER reject_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.Join(ctx, linkCode(t, inv), "carol", "", "correct horse battery"); err != nil {
		t.Errorf("joining once the log can be written: %v", err)
	}
	if out, err := e.Bridge.RenewToken(ctx, renewer, "api"); err != nil || out.Secret == "" {
		t.Errorf("renewing once the log can be written: %+v %v", out, err)
	}
}
