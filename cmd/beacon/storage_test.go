package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

// a configuration with a database of its own, for the storage commands
func storageTestConfig(t *testing.T) (cfg string, dbPath string) {
	t.Helper()
	d := t.TempDir()
	tok := filepath.Join(d, "tok")
	if err := os.WriteFile(tok, []byte("0123456789abcdef0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(d, "data")
	cfg = filepath.Join(d, "config.yaml")
	body := "data_dir: " + filepath.ToSlash(data) +
		"\nrules: {token_file: " + filepath.ToSlash(tok) + "}\nvictoriametrics: {url: http://vm:8428}\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg, filepath.Join(data, "beacon.db")
}

func TestAuditVerifyCLI(t *testing.T) {
	cfg, dbPath := storageTestConfig(t)
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if err := st.Audit(store.AuditEntry{Actor: "alice", Action: "ack", Target: strings.Repeat("g", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CheckpointAudit(); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	var out, errb bytes.Buffer
	if code := run([]string{"audit", "verify", "-config", cfg}, &out, &errb); code != 0 || !strings.Contains(out.String(), "audit chain OK: 5 records") {
		t.Fatalf("verify: %d %s %s", code, out.String(), errb.String())
	}
	// a tampered entry is caught: the record no longer matches what it says of itself
	db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteHandle().Exec(`UPDATE audit_events SET detail = 'not what happened' WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out.Reset()
	if code := run([]string{"audit", "verify", "-config", cfg}, &out, &errb); code != 1 || !strings.Contains(out.String(), "broken") {
		t.Fatalf("a tampered chain: %d %s %s", code, out.String(), errb.String())
	}
}

func TestStorageCheckCLI(t *testing.T) {
	cfg, dbPath := storageTestConfig(t)
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateToken("probe", []string{"status:read"}, 24*3600*1e9, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(store.AuditEntry{Actor: "alice", Action: "ack"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"storage", "check", "-config", cfg}, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "integrity: ok") || !strings.Contains(out.String(), "tokens 1") ||
		!strings.Contains(out.String(), "audit chain: OK, 1 records") {
		t.Fatalf("storage check: %d %s %s", code, out.String(), errb.String())
	}
	// without a database, it says so
	cfg2, _ := storageTestConfig(t)
	out.Reset()
	errb.Reset()
	if code := run([]string{"storage", "check", "-config", cfg2}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no database at") {
		t.Fatalf("no database: %d %s %s", code, out.String(), errb.String())
	}
}
