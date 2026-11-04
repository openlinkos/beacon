package backup

// The manager against a real store: a database with groups, tokens and audit entries, a
// recordings directory, a configuration and a secrets directory; a backup; the database deleted
// (the thing backups exist for); a restore from the archive; and everything compared, item by
// item. The plan's acceptance for this package, in miniature, on every test run.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
)

// fixtureStore fills a store the way a live one looks: groups, machines, audit entries.
func fixtureStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		g := store.Group{ID: fmtGroup(i), Key: fmtGroup(i), Labels: map[string]string{"alertname": "FixtureDown", "fixture": string(rune('a' + i))}}
		if err := st.PutGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutMachine(store.Machine{Name: "edge-1", Address: "example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if err := st.Audit(store.AuditEntry{Actor: "tester", Via: "cli", Action: "fixture.entry", Target: fmtGroup(i % 3)}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func fmtGroup(i int) string { return "fixture-group-" + string(rune('0'+i)) }

func testOptions(t *testing.T, dataDir, destDir string, recipients []age.Recipient) Options {
	t.Helper()
	return Options{
		Host: "test-host", Version: "test", DBPath: filepath.Join(dataDir, "beacon.db"),
		DataDir: dataDir, ConfigFile: filepath.Join(dataDir, "config.yaml"),
		CatalogDir: filepath.Join(dataDir, "catalog"), TargetsDir: filepath.Join(dataDir, "targets"),
		SecretsDir:   filepath.Join(dataDir, "secrets"),
		Recipients:   recipients,
		Destinations: []Destination{{Name: destDir, Dir: destDir}},
		ScheduleDB:   time.Hour, ScheduleFiles: 24 * time.Hour, ScheduleFull: 7 * 24 * time.Hour,
		Retention: DefaultKeep(), Retries: 1, RetryWait: time.Millisecond, VerifyReadback: true,
		HeartbeatMaxAge: 2 * time.Hour, HeartbeatSeverity: "warning",
		DrillExpiry: 90 * 24 * time.Hour,
	}
}

func fixtureTree(t *testing.T, dataDir string) {
	t.Helper()
	for name, content := range map[string]string{
		"config.yaml":               "listen: :17001\ndata_dir: " + dataDir + "\n",
		"catalog/locations.yaml":    "locations: []\n",
		"targets/a.yaml":            "targets: []\n",
		"secrets/root-key":          fixtureSecret,
		"recordings/day-1/old.cast": "a recording fixture\n",
	} {
		p := filepath.Join(dataDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	destDir := t.TempDir()
	fixtureTree(t, dataDir)
	st := fixtureStore(t, filepath.Join(dataDir, "beacon.db"))
	counts := fixtureStoreCounts(t, filepath.Join(dataDir, "beacon.db"))
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := New(testOptions(t, dataDir, destDir, []age.Recipient{id.Recipient()}))
	if err != nil {
		t.Fatal(err)
	}
	man, err := mgr.Run(context.Background(), "manual", "tester", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if man.Kind != "full" { // the first run takes everything
		t.Fatalf("the first run was %s, not full", man.Kind)
	}
	if man.Recordings != 1 || len(man.Tables) == 0 || man.AuditHead == "" {
		t.Fatalf("manifest says little: %+v", man)
	}
	if got := man.Tables["kv/groups"]; got != 3 {
		t.Fatalf("the manifest counts %d groups, the store held 3", got)
	}

	// a second run after a new recording is incremental: only the new file travels
	newRec := filepath.Join(dataDir, "recordings", "day-2", "new.cast")
	if err := os.MkdirAll(filepath.Dir(newRec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newRec, []byte("second recording\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	man2, err := mgr.Run(context.Background(), "schedule", "system", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if man2.Kind != "incr" || man2.Recordings != 1 {
		t.Fatalf("the second run was %s with %d recordings, not incr with the new one", man2.Kind, man2.Recordings)
	}

	// the loss backups exist for: the database and the recordings go
	for _, gone := range []string{"beacon.db", "beacon.db-wal", "beacon.db-shm", "recordings"} {
		if err := os.RemoveAll(filepath.Join(dataDir, gone)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "beacon.db")); !os.IsNotExist(err) {
		t.Fatal("the database is still there")
	}

	// the recovery: full first, then the increment, with the identity
	entries, err := Destination{Dir: destDir}.List(context.Background())
	if err != nil || len(entries) != 2 {
		t.Fatalf("the destination lists %d backups: %v", len(entries), err)
	}
	fullPath := filepath.Join(destDir, entries[1].Name) // newest first
	incrPath := filepath.Join(destDir, entries[0].Name)
	res, err := Restore(context.Background(), []string{fullPath, incrPath}, []age.Identity{id}, filepath.Join(dataDir, "recovered"))
	if err != nil {
		t.Fatal(err)
	}
	// every member of both archives is back, including both recordings
	for _, name := range []string{
		"recordings/day-1/old.cast", "recordings/day-2/new.cast",
		"secrets/root-key", "config/config.yaml", "config/catalog/locations.yaml", "config/targets/a.yaml",
	} {
		if _, err := os.Stat(filepath.Join(res.Dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s did not come back: %v", name, err)
		}
	}
	sec, err := os.ReadFile(filepath.Join(res.Dir, "secrets/root-key"))
	if err != nil || string(sec) != fixtureSecret {
		t.Errorf("the secrets member came back wrong")
	}
	// the restored database opens and holds exactly what the live one did, and its chain verifies
	restored := fixtureStoreCounts(t, filepath.Join(dataDir, "recovered", "db", "beacon.db"))
	if restored["kv/groups"] != counts["kv/groups"] || restored["kv/machines"] != counts["kv/machines"] ||
		restored["audit_events"] != counts["audit_events"] {
		t.Fatalf("the restored database holds %v, the live one held %v", restored, counts)
	}
	if res, err := VerifyAudit(context.Background(), filepath.Join(dataDir, "recovered", "db", "beacon.db")); err != nil || res.Events != 5 {
		t.Fatalf("the restored chain: %d events, err %v", res.Events, err)
	}
}

func fixtureStoreCounts(t *testing.T, path string) map[string]int64 {
	t.Helper()
	_, tables, err := Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return tables
}

func TestBuiltinsAddHeartbeatAndDrillResource(t *testing.T) {
	dataDir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := New(testOptions(t, dataDir, t.TempDir(), []age.Recipient{id.Recipient()}))
	if err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{}
	with := mgr.Builtins(cat)
	if len(with.Heartbeats) != 1 || with.Heartbeats[0].Name != HeartbeatName {
		t.Fatalf("built-in heartbeat: %+v", with.Heartbeats)
	}
	if with.Heartbeats[0].MaxAge.D() != 2*time.Hour {
		t.Fatalf("the built-in heartbeat's max_age is %s", with.Heartbeats[0].MaxAge)
	}
	if len(with.Resources) != 1 || with.Resources[0].Name != ResourceDrill {
		t.Fatalf("built-in resource: %+v", with.Resources)
	}
	if with.Resources[0].ExpiresAt().IsZero() || with.Resources[0].ExpiresAt().Before(mgr.now()) {
		t.Fatalf("the built-in resource's expiry did not parse: %v", with.Resources[0].ExpiresAt())
	}
	if len(cat.Heartbeats) != 0 || len(cat.Resources) != 0 {
		t.Fatal("the caller's catalogue was changed")
	}
	// a deployment's own heartbeat stands
	own := &catalog.Catalog{Heartbeats: []catalog.Heartbeat{{Name: HeartbeatName, MaxAge: config.Duration(time.Hour), Severity: "critical"}}}
	if got := mgr.Builtins(own); len(got.Heartbeats) != 1 || got.Heartbeats[0].MaxAge.D() != time.Hour {
		t.Fatal("the deployment's own heartbeat was overridden")
	}
}

func TestEnsureKeyWritesRecoveryMaterialOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backup")
	rs, generated, err := EnsureKey(dir, nil)
	if err != nil || !generated || len(rs) != 1 {
		t.Fatalf("ensure: %d %v %v", len(rs), generated, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "RECOVERY-MATERIAL.txt")); err != nil {
		t.Fatal("the recovery material was not written")
	}
	if _, err := os.Stat(filepath.Join(dir, "age-public.key")); err != nil {
		t.Fatal("the public key was not kept")
	}
	pub, err := os.ReadFile(filepath.Join(dir, "age-public.key"))
	if err != nil || len(pub) == 0 {
		t.Fatal(err)
	}
	// the second call finds the stored public key and writes nothing new
	rs2, generated2, err := EnsureKey(dir, nil)
	if err != nil || generated2 || len(rs2) != 1 {
		t.Fatalf("second ensure: %d %v %v", len(rs2), generated2, err)
	}
	// configured recipients win; nothing is generated then
	own, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	rs3, generated3, err := EnsureKey(dir, []age.Recipient{own.Recipient()})
	if err != nil || generated3 || len(rs3) != 1 {
		t.Fatalf("configured recipients were not used as they are")
	}
}

// The recovery material itself — prose and instructions around the key — is what an operator
// hands to -identity; parsing it must find the key, and the key must open what the public half
// sealed.
func TestIdentityFromRecoveryMaterial(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ids, err := ParseIdentities([]byte(RecoveryMaterial(k, "host-1")))
	if err != nil || len(ids) != 1 {
		t.Fatalf("the material parsed: %d %v", len(ids), err)
	}
	dir := t.TempDir()
	src := writeSource(t, dir, "db/beacon.db", "fixture")
	recs, err := ParseRecipients([]string{k.Public})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "a"+Suffix)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(f, recs, []Source{src}, Manifest{Kind: "full", Created: timeNow().UTC()}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := Extract(out, ids, filepath.Join(dir, "out")); err != nil {
		t.Fatalf("the material's key did not open the archive: %v", err)
	}
}

func TestServiceRunningDetectsTheLock(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "beacon.db")
	if ServiceRunning(db) {
		t.Fatal("nothing holds the lock of a database that does not exist")
	}
	release, err := lockExclusive(db + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if !ServiceRunning(db) {
		t.Fatal("the held lock was not seen")
	}
	release()
	if ServiceRunning(db) {
		t.Fatal("the released lock still reads as held")
	}
}

func TestSnapshotAndIntegrity(t *testing.T) {
	dataDir := t.TempDir()
	st := fixtureStore(t, filepath.Join(dataDir, "beacon.db"))
	defer func() { _ = st.Close() }()
	snap := filepath.Join(dataDir, "snap.db")
	schema, tables, err := Snapshot(context.Background(), filepath.Join(dataDir, "beacon.db"), snap)
	if err != nil {
		t.Fatal(err)
	}
	if schema == 0 || tables["kv/groups"] != 3 || tables["audit_events"] != 5 {
		t.Fatalf("snapshot: schema %d tables %v", schema, tables)
	}
	if err := IntegrityCheck(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	res, err := VerifyAudit(context.Background(), snap)
	if err != nil || res.Events != 5 {
		t.Fatalf("the snapshot's chain: %d events, %v", res.Events, err)
	}
	// a snapshot over an existing file refuses, rather than half-writing
	if _, _, err := Snapshot(context.Background(), filepath.Join(dataDir, "beacon.db"), snap); err == nil {
		t.Fatal("a snapshot over an existing file was taken")
	}
}
