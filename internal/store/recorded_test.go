package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// rejectAudit makes every audit entry fail to be written to the database open(t) opened, on SQLite or
// PostgreSQL, until the function it returns is called.
func rejectAudit(t *testing.T, path string) func() {
	t.Helper()
	driver, dsn := "sqlite", path
	create := []string{"CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"}
	drop := "DROP TRIGGER reject_audit"
	if currentDSN != "" {
		driver, dsn = "pgx", currentDSN
		create = []string{
			"CREATE OR REPLACE FUNCTION reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test write rejection'; END $$",
			"CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_audit()",
		}
		drop = "DROP TRIGGER reject_audit ON audit_events"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range create {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		t.Helper()
		if _, err := db.Exec(drop); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRecordedCredentialsRollBack checks that joining (the first owner with the setup link, or someone
// with an invitation), renewing a token, making an agent with its keys, giving an agent a token and
// adding a key to a person or an agent are made together with their audit entries: when an entry cannot
// be written, nothing is changed (ErrUnrecorded), and the link and the secret still work afterwards.
func TestRecordedCredentialsRollBack(t *testing.T) {
	s, path := open(t)
	entry := func(action string) []AuditEntry { return []AuditEntry{{Actor: "test", Via: "cli", Action: action}} }
	unrecorded := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrUnrecorded) {
			t.Errorf("%s without its record: %v", what, err)
		}
	}

	setup, _, err := s.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	allow := rejectAudit(t, path)
	_, err = s.JoinRecorded(setup, "olive", "", "correct horse battery", func(Account) []AuditEntry { return entry("setup") })
	unrecorded("the first owner", err)
	if len(s.Accounts()) != 0 {
		t.Fatalf("an owner was made without its record: %+v", s.Accounts())
	}
	allow()
	if _, err := s.JoinRecorded(setup, "olive", "", "correct horse battery", func(Account) []AuditEntry { return entry("setup") }); err != nil {
		t.Fatalf("the setup link once the log can be written: %v", err)
	}
	inv, _, err := s.CreateInvitation("oncall", "", LinkMaker{Name: "olive", Key: "user:olive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAgent(Agent{Name: "codex", Owner: "olive", Preset: "readonly"}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := s.CreateToken("renewing", []string{"status:read"}, 30*24*time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	key := SSHKey{Key: "ssh-ed25519 AAAArecorded", Fingerprint: "SHA256:recorded"}

	allow = rejectAudit(t, path)
	_, err = s.JoinRecorded(inv, "rita", "", "correct horse battery", func(Account) []AuditEntry { return entry("user_join") })
	unrecorded("joining with an invitation", err)
	_, _, err = s.RenewTokenRecorded("renewing", func(Token) []AuditEntry { return entry("token_renew") })
	unrecorded("a renewal", err)
	_, err = s.CreateAgentRecorded(Agent{Name: "gemini", Owner: "olive", Preset: "readonly"}, []SSHKey{key}, func(Agent) []AuditEntry { return entry("agent_create") })
	unrecorded("an agent with a key", err)
	_, _, err = s.CreateAgentTokenRecorded("codex", 24*time.Hour, "test", func(Token) []AuditEntry { return entry("agent_token") })
	unrecorded("an agent's token", err)
	unrecorded("a person's key", s.AddAccountSSHKeyRecorded("olive", key, func(_, _ Account) []AuditEntry { return entry("ssh_key_add") }))
	_, err = s.AddAgentSSHKeyRecorded("codex", key, func(_, _ Agent) []AuditEntry { return entry("ssh_key_add") })
	unrecorded("an agent's key", err)

	if _, ok := s.Account("rita"); ok {
		t.Error("an account was made without its record")
	}
	if _, ok := s.VerifyToken(secret); !ok {
		t.Error("the token lost its secret to a renewal without a record")
	}
	if _, ok := s.Agent("gemini"); ok {
		t.Error("an agent was made without its record")
	}
	if h := s.KeyHolder(key.Fingerprint); h != "" {
		t.Errorf("a key was added without its record, to %s", h)
	}
	for _, tok := range s.Tokens() {
		if tok.Agent == "codex" || tok.Name == "renewing" && !tok.Renewed.IsZero() {
			t.Errorf("a token was changed without its record: %+v", tok)
		}
	}

	allow()
	if _, err := s.JoinRecorded(inv, "rita", "", "correct horse battery", func(Account) []AuditEntry { return entry("user_join") }); err != nil {
		t.Errorf("the invitation once the log can be written: %v", err)
	}
	if _, _, err := s.RenewTokenRecorded("renewing", func(Token) []AuditEntry { return entry("token_renew") }); err != nil {
		t.Errorf("a renewal once the log can be written: %v", err)
	}
	a, err := s.CreateAgentRecorded(Agent{Name: "gemini", Owner: "olive", Preset: "readonly"}, []SSHKey{key, key}, func(Agent) []AuditEntry { return entry("agent_create") })
	if err != nil || len(a.SSHKeys) != 1 || s.KeyHolder(key.Fingerprint) == "" {
		t.Errorf("an agent with a key once the log can be written: %+v %v", a, err)
	}
	if _, err := s.CreateAgentRecorded(Agent{Name: "claude", Owner: "olive", Preset: "readonly"}, []SSHKey{key}, nil); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("an agent with another's key: %v", err)
	}
	var found []string
	for _, e := range s.AuditLog(100) {
		if e.Actor == "test" {
			found = append(found, e.Action)
		}
	}
	if len(found) != 4 { // setup, user_join, token_renew, agent_create
		t.Errorf("the entries written with their changes: %v", found)
	}
}
