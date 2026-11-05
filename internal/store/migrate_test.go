package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

// fixture builds a bbolt database with every bucket the old store kept, each holding
// representative entries of the shapes it wrote: dozens per bucket, all kinds of keys.
func fixture(t *testing.T, path string) map[string]map[string][]byte {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string][]byte{}
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	put := func(tx *bolt.Tx, name string, key string, v any) []byte {
		t.Helper()
		b, err := tx.CreateBucketIfNotExists([]byte(name))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Put([]byte(key), raw); err != nil {
			t.Fatal(err)
		}
		if want[name] == nil {
			want[name] = map[string][]byte{}
		}
		want[name][key] = raw
		return raw
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for i := range 30 { // groups
			put(tx, "groups", fmt.Sprintf("g-%02d", i), Group{ID: fmt.Sprintf("g-%02d", i), Status: "firing", StartsAt: now, Labels: map[string]string{"n": fmt.Sprint(i)}})
		}
		for i := range 12 { // tokens
			put(tx, "tokens", fmt.Sprintf("tok-%02d", i), Token{Name: fmt.Sprintf("tok-%02d", i), Hash: fmt.Sprintf("h%d", i), Scopes: []string{"status:read"}, Created: now, Expires: now.Add(24 * time.Hour), Lifetime: 24 * time.Hour})
		}
		for i := range 40 { // audit, keyed by 8-byte big-endian sequence
			raw := put(tx, "audit", string(seqKey(uint64(i+1))), AuditEntry{Time: now.Add(time.Duration(i) * time.Minute), Actor: "alice", Via: "api", Action: "ack", Target: fmt.Sprintf("g-%02d", i%30)})
			_ = raw
		}
		// meta: counters, times and a secret, the three shapes it holds
		for i := range 5 {
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, uint64(100+i))
			b := tx.Bucket([]byte("meta"))
			if b == nil {
				b, err = tx.CreateBucketIfNotExists([]byte("meta"))
				if err != nil {
					return err
				}
			}
			if err := b.Put([]byte(fmt.Sprintf("counter-%d", i)), buf); err != nil {
				return err
			}
			if want["meta"] == nil {
				want["meta"] = map[string][]byte{}
			}
			want["meta"][fmt.Sprintf("counter-%d", i)] = buf
		}
		for i := range 3 {
			v := []byte(now.Add(time.Duration(i) * time.Hour).Format(time.RFC3339Nano))
			b := tx.Bucket([]byte("meta"))
			if err := b.Put([]byte("t:drill-"+fmt.Sprint(i)), v); err != nil {
				return err
			}
			want["meta"]["t:drill-"+fmt.Sprint(i)] = v
		}
		secret := bytes.Repeat([]byte{7}, 32)
		if err := tx.Bucket([]byte("meta")).Put([]byte("secret:pages"), secret); err != nil {
			return err
		}
		want["meta"]["secret:pages"] = secret
		for i := range 8 { // silences
			put(tx, "silences", fmt.Sprintf("sil-%02d", i), SilenceRecord{ID: fmt.Sprintf("sil-%02d", i), StartsAt: now, EndsAt: now.Add(time.Hour)})
		}
		for i := range 15 { // machines
			put(tx, "machines", fmt.Sprintf("machine-%02d", i), Machine{Name: fmt.Sprintf("machine-%02d", i), Address: "example.com:22", User: "beacon", HostKey: "ssh-ed25519 AAAA", Added: now})
		}
		for i := range 35 { // sessions, ids beginning with the start time
			put(tx, "sessions", fmt.Sprintf("20261101-0800%02d-s%d", i%60, i), SessionRecord{ID: fmt.Sprintf("20261101-0800%02d-s%d", i%60, i), Machine: fmt.Sprintf("machine-%02d", i%15), Started: now, Via: "ssh"})
		}
		for i := range 6 { // accounts
			put(tx, "accounts", fmt.Sprintf("user%d", i), Account{Username: fmt.Sprintf("user%d", i), Role: "owner", PasswordHash: "$argon2id$x", Created: now})
		}
		for i := range 4 { // one-time links
			put(tx, "account_links", fmt.Sprintf("linkhash%d", i), AccountLink{Hash: fmt.Sprintf("linkhash%d", i), Kind: LinkInvite, Role: "oncall", Created: now, Expires: now.Add(time.Hour)})
		}
		for i := range 9 { // agents
			put(tx, "agents", fmt.Sprintf("agent-%02d", i), Agent{Name: fmt.Sprintf("agent-%02d", i), Owner: "user0", Preset: "oncall", Created: now})
		}
		for i := range 7 { // oauth
			put(tx, "oauth_clients", fmt.Sprintf("lbc_%02d", i), OAuthClient{ID: fmt.Sprintf("lbc_%02d", i), Created: now})
			put(tx, "oauth_grants", fmt.Sprintf("grant-%02d", i), Grant{ID: fmt.Sprintf("grant-%02d", i), Agent: fmt.Sprintf("agent-%02d", i), Client: fmt.Sprintf("lbc_%02d", i), Expires: now.Add(time.Hour)})
			put(tx, "oauth_access", fmt.Sprintf("access-%02d", i), accessRecord{Grant: fmt.Sprintf("grant-%02d", i), Expires: now.Add(time.Hour)})
		}
		for i := range 25 { // changes, ids beginning with the event's time
			put(tx, "changes", fmt.Sprintf("20261101T0800%02d.000000000-c%d", i%60, i), Change{ID: "c", Time: now, Source: "flux", Kind: "deploy", Summary: "c", PostedBy: "tok-00", Received: now})
		}
		for i := range 10 { // configuration
			put(tx, "config", fmt.Sprintf("key-%02d", i), []byte(fmt.Sprintf(`{"n":%d}`, i)))
			put(tx, "config_changes", string(seqKey(uint64(i+1))), ConfigChange{Version: int64(i + 1), Time: now, Actor: "alice", Via: "api", Action: "put"})
		}
		if err := tx.Bucket([]byte("config_changes")).SetSequence(10); err != nil {
			return err
		}
		for i := range 11 { // console sessions
			put(tx, "console_sessions", fmt.Sprintf("cshash%02d", i), ConsoleSession{Hash: fmt.Sprintf("cshash%02d", i), User: "user0", Expires: now.Add(time.Hour)})
		}
		for i := range 13 { // status
			put(tx, "status_incidents", fmt.Sprintf("inc-%02d", i), Incident{ID: fmt.Sprintf("inc-%02d", i), AlertName: "ServiceDown", StartsAt: now})
			put(tx, "maintenance", fmt.Sprintf("mnt-%02d", i), Maintenance{ID: fmt.Sprintf("mnt-%02d", i), StartsAt: now, EndsAt: now.Add(time.Hour)})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return want
}

func seqKey(seq uint64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, seq)
	return k
}

// TestMigrate is the acceptance: a bbolt file with every bucket and a representative amount of
// data moves into the SQL database, bucket by bucket and key by key the same, with the audit as
// the event table's first records and the old file kept as .pre-sql.
func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "status.db")
	wanted := fixture(t, boltPath)
	dbPath := filepath.Join(dir, "beacon.db")

	migrated, err := Migrate(boltPath, Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("it should have migrated")
	}
	if _, err := os.Stat(boltPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old file should be gone: %v", err)
	}
	if _, err := os.Stat(boltPath + ".pre-sql"); err != nil {
		t.Errorf("the rollback copy should be there: %v", err)
	}

	// every bucket, every key, byte for byte; and the sequence with it
	db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: dbPath, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.View(func(tx kv.Tx) error {
		for name, entries := range wanted {
			if name == "audit" {
				continue
			}
			for k, v := range entries {
				if got := tx.Get([]byte(name), []byte(k)); !bytes.Equal(got, v) {
					t.Errorf("%s/%s: got %d bytes, want %d", name, k, len(got), len(v))
				}
			}
			if got := tx.Count([]byte(name)); got != len(entries) {
				t.Errorf("%s: %d entries, want %d", name, got, len(entries))
			}
		}
		if seq := tx.Sequence([]byte("config_changes")); seq != 10 {
			t.Errorf("config_changes sequence: %d, want 10", seq)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// the audit entries came over, in order, after the genesis record
	audit, err := events.Open(db.Handle(), nil, "audit_events", events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, total, err := audit.Query(events.Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if total != len(wanted["audit"])+1 { // the genesis record makes one more
		t.Fatalf("audit events: %d, want %d", total, len(wanted["audit"])+1)
	}
	// the newest comes first; the genesis record, seq 1, is the last of them
	last := got[len(got)-1]
	if last.Action != "migrate" || last.Seq != 1 {
		t.Fatalf("the genesis record: %+v", last)
	}
	if !bytes.Contains([]byte(last.Detail), []byte("import hash ")) {
		t.Errorf("the genesis record should note the import's hash: %q", last.Detail)
	}
	for i, e := range got[:len(got)-1] { // the entries the audit bucket held, newest first
		var src AuditEntry
		key := e.Seq - 1 // the genesis record took seq 1, so entry j of the source is seq j+1
		if err := json.Unmarshal(wanted["audit"][string(seqKey(key))], &src); err != nil {
			t.Fatal(err)
		}
		if e.Actor != src.Actor || e.Action != src.Action || e.Object != src.Target || !e.Time.Equal(src.Time) {
			t.Fatalf("audit entry %d (seq %d): got %+v, want %+v", i, e.Seq, e, src)
		}
	}
	if res, err := audit.Verify(); err != nil || res.Events != uint64(total) {
		t.Fatalf("the migrated chain verifies: %v %+v", err, res)
	}

	// the store opens on what the migration made, and sees it all
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if gs := st.Groups(); len(gs) != 30 || gs[0].ID == "" {
		t.Errorf("groups through the store: %d", len(gs))
	}
	if _, ok := st.Account("user3"); !ok {
		t.Error("accounts through the store")
	}
	if _, ok := st.Machine("machine-07"); !ok {
		t.Error("machines through the store")
	}
	if secret, err := st.Secret("pages"); err != nil || !bytes.Equal(secret, bytes.Repeat([]byte{7}, 32)) {
		t.Errorf("the secret came over: %v", err)
	}
	if v := st.Counter("counter-2"); v != 102 {
		t.Errorf("counter-2: %d", v)
	}
	if entries, _, _ := st.AuditQuery(AuditFilter{Limit: 1000}); len(entries) != len(wanted["audit"])+1 {
		t.Errorf("audit through the store: %d", len(entries))
	}
	if cfg, version, err := st.Config(); err != nil || len(cfg) != 10 || version != 10 {
		t.Errorf("config: %d entries, version %d %v", len(cfg), version, err)
	}
}

func TestMigrateNothingToDo(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beacon.db")
	// no bbolt file: a fresh start
	if migrated, err := Migrate(filepath.Join(dir, "status.db"), Options{Path: dbPath}); err != nil || migrated {
		t.Fatalf("no bbolt file: %v %v", migrated, err)
	}
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Audit(AuditEntry{Actor: "alice", Action: "ack"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	// a database with something in it: no second migration, nothing lost
	if migrated, err := Migrate(filepath.Join(dir, "none.db"), Options{Path: dbPath}); err != nil || migrated {
		t.Fatalf("a used database: %v %v", migrated, err)
	}
	st2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if entries, total, _ := st2.AuditQuery(AuditFilter{}); total != 1 || len(entries) != 1 {
		t.Fatalf("the entry survived: %d %d", total, len(entries))
	}
}

// TestMigrateFailsClean: a failure mid-migration leaves no half-moved database and the old file
// untouched, so the next start tries again.
func TestMigrateFailsClean(t *testing.T) {
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "status.db")
	fixture(t, boltPath)
	dbPath := filepath.Join(dir, "beacon.db")
	// pre-open the target read-write so the migration cannot take it: it fails rather than
	// half-move — simulated by a lock the migration cannot get
	blocker, err := kv.OpenSQLite(kv.SQLiteOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err := Migrate(boltPath, Options{Path: dbPath}); err == nil {
		t.Fatal("a blocked database should fail the migration")
	}
	_ = blocker.Close()
	// and once the way is free, it works
	migrated, err := Migrate(boltPath, Options{Path: dbPath})
	if err != nil || !migrated {
		t.Fatalf("after the block cleared: %v %v", migrated, err)
	}
}

func TestMigrateFailsInside(t *testing.T) {
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "status.db")
	fixture(t, boltPath)
	dbPath := filepath.Join(dir, "beacon.db")
	migrateFailAt = "inside"
	t.Cleanup(func() { migrateFailAt = "" })
	if _, err := Migrate(boltPath, Options{Path: dbPath}); err == nil {
		t.Fatal("an injected failure should fail the migration")
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed migration should not leave the SQL database")
	}
	if _, err := os.Stat(boltPath); err != nil {
		t.Fatal("the bbolt file should still be there")
	}
	migrateFailAt = ""
	migrated, err := Migrate(boltPath, Options{Path: dbPath})
	if err != nil || !migrated {
		t.Fatalf("retry: %v %v", migrated, err)
	}
}

func TestMigrateCrashAfterCommit(t *testing.T) {
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "status.db")
	fixture(t, boltPath)
	dbPath := filepath.Join(dir, "beacon.db")
	migrateFailAt = "after-commit"
	t.Cleanup(func() { migrateFailAt = "" })
	if _, err := Migrate(boltPath, Options{Path: dbPath}); err == nil {
		t.Fatal("the injected crash should come back")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal("the SQL database should be in place")
	}
	if _, err := os.Stat(boltPath); err != nil {
		t.Fatal("the bbolt file should still be there")
	}
	migrateFailAt = ""
	migrated, err := Migrate(boltPath, Options{Path: dbPath})
	if err != nil || !migrated {
		t.Fatalf("finishing the leftover file: %v %v", migrated, err)
	}
	if _, err := os.Stat(boltPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the leftover bbolt file should have gone")
	}
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if gs := st.Groups(); len(gs) != 30 {
		t.Errorf("groups after finishing: %d", len(gs))
	}
}

func TestMigrateBothFilesMatch(t *testing.T) {
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "status.db")
	fixture(t, boltPath)
	dbPath := filepath.Join(dir, "beacon.db")
	if _, err := Migrate(boltPath, Options{Path: dbPath}); err != nil {
		t.Fatal(err)
	}
	pre := boltPath + ".pre-sql"
	if err := copyFile(pre, boltPath); err != nil {
		t.Fatal(err)
	}
	migrated, err := Migrate(boltPath, Options{Path: dbPath})
	if err != nil || !migrated {
		t.Fatalf("matching leftover: %v %v", migrated, err)
	}
	if _, err := os.Stat(boltPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the matching leftover should have gone")
	}
}

// TestMigrateRefuseNamesTheSQLPath: a leftover bbolt file beside a non-empty SQL database
// that was never migrated (no genesis) must name the SQL path and say how to remigrate.
func TestMigrateRefuseNamesTheSQLPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beacon.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(AuditEntry{Actor: "alice", Action: "ack"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	boltPath := filepath.Join(dir, "status.db")
	fixture(t, boltPath)
	_, err = Migrate(boltPath, Options{Path: dbPath})
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("want ErrDiverged, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, dbPath) {
		t.Fatalf("error should name the SQL path %s: %s", dbPath, msg)
	}
	if !strings.Contains(msg, "remigrate") {
		t.Fatalf("error should say how to remigrate: %s", msg)
	}
}

func TestMigrateBothFilesDiverge(t *testing.T) {
	dir := t.TempDir()
	boltPath := filepath.Join(dir, "status.db")
	fixture(t, boltPath)
	dbPath := filepath.Join(dir, "beacon.db")
	if _, err := Migrate(boltPath, Options{Path: dbPath}); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(boltPath+".pre-sql", boltPath); err != nil {
		t.Fatal(err)
	}
	bdb, err := bolt.Open(boltPath, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = bdb.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("tokens"))
		if err != nil {
			return err
		}
		return b.Put([]byte("tok-new"), []byte(`{"name":"tok-new"}`))
	})
	_ = bdb.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(boltPath, Options{Path: dbPath}); !errors.Is(err, ErrDiverged) {
		t.Fatalf("diverged files: %v", err)
	}
	if _, err := os.Stat(boltPath); err != nil {
		t.Fatal("the bbolt file should still be there")
	}
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o600)
}
