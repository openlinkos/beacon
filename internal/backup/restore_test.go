package backup

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func TestRestoreWithoutConfirmIsRefused(t *testing.T) {
	dir := t.TempDir()
	rec, id, path, signer := oneArchive(t, dir)
	_ = rec
	_, err := Restore(context.Background(), RestoreOpts{
		Paths: []string{path}, Identities: []age.Identity{id}, Verify: signPublicOf(signer),
		Dir: filepath.Join(dir, "live"), Live: true, Confirm: false,
	})
	if err == nil || !errors.Is(err, ErrNeedConfirm) {
		t.Fatalf("a live restore without confirm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "live", "db", "beacon.db")); err == nil {
		t.Fatal("a refused restore still wrote a database")
	}
}

func TestRestoreFailureDoesNotLeaveAHalfAppliedTree(t *testing.T) {
	dir := t.TempDir()
	_, id, path, signer := oneArchive(t, dir)
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { failApplyName = "" })
	failApplyName = "secrets/root-key"
	err := error(nil)
	_, err = Restore(context.Background(), RestoreOpts{
		Paths: []string{path}, Identities: []age.Identity{id}, Verify: signPublicOf(signer),
		Dir: dest,
	})
	if err == nil {
		t.Fatal("a restore that was asked to fail succeeded")
	}
	if _, err := os.Stat(filepath.Join(dest, "db", "beacon.db")); err == nil {
		t.Fatal("a failed restore left a database member in place")
	}
}

func TestRestoreRefusesNewerDataVersion(t *testing.T) {
	dir := t.TempDir()
	rec, id, _ := testKeys(t)
	signer := testSigner(t)
	src := writeSource(t, dir, "db/beacon.db", "fixture")
	out := filepath.Join(dir, "a"+Suffix)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(f, []age.Recipient{rec}, signer, []Source{src},
		Manifest{Kind: KindFull, Created: time.Now().UTC(), DataVersion: DataVersion + 1}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_, err = Restore(context.Background(), RestoreOpts{
		Paths: []string{out}, Identities: []age.Identity{id}, Verify: signPublicOf(signer),
		Dir: filepath.Join(dir, "out"),
	})
	if err == nil || !errors.Is(err, ErrDataVersion) {
		t.Fatalf("a newer data version was restored: %v", err)
	}
}

func TestPlaceDatabaseReportsWALDeleteFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "member.db")
	if err := os.WriteFile(src, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	wal := filepath.Join(dir, "beacon.db-wal")
	if err := os.Mkdir(wal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wal, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := PlaceDatabase(src, dir)
	if err == nil || !strings.Contains(err.Error(), "the database was not replaced because the old WAL could not be removed") {
		t.Fatalf("a WAL that could not be removed: %v", err)
	}
	if _, stErr := os.Stat(filepath.Join(dir, "beacon.db")); stErr == nil {
		t.Fatal("the database was replaced even though the old WAL could not be removed")
	}
}

func TestRestoreLiveCopiesAsideAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "data")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "beacon.db"), []byte("live-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, id, path, signer := oneArchive(t, dir)
	t.Cleanup(func() { failApplyName = "" })
	failApplyName = "secrets/root-key"
	_, err := Restore(context.Background(), RestoreOpts{
		Paths: []string{path}, Identities: []age.Identity{id}, Verify: signPublicOf(signer),
		Dir: live, Live: true, Confirm: true,
		Now: func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) },
	})
	if err == nil {
		t.Fatal("a live restore that was asked to fail succeeded")
	}
	if !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("wanted injected failure, got: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(live, "beacon.db"))
	if err != nil || string(got) != "live-db" {
		t.Fatalf("the live database was not rolled back: %q %v", got, err)
	}
}

func TestRestoreLiveAsideIsInsideTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "data")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "beacon.db"), []byte("live-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, id, path, signer := oneArchive(t, dir)
	res, err := Restore(context.Background(), RestoreOpts{
		Paths: []string{path}, Identities: []age.Identity{id}, Verify: signPublicOf(signer),
		Dir: live, Live: true, Confirm: true,
		Now: func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("live restore: %v", err)
	}
	if res.Aside == "" {
		t.Fatal("no aside copy")
	}
	liveAbs, err := filepath.Abs(live)
	if err != nil {
		t.Fatal(err)
	}
	asideAbs, err := filepath.Abs(res.Aside)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(liveAbs, asideAbs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		t.Fatalf("aside %s is not inside the data directory %s", asideAbs, liveAbs)
	}
}

func oneArchive(t *testing.T, dir string) (age.Recipient, age.Identity, string, ed25519.PrivateKey) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	signer := testSigner(t)
	dbPath := filepath.Join(dir, "beacon.db")
	st := fixtureStore(t, dbPath)
	_ = st.Close()
	audit, err := VerifyAudit(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	src := Source{Name: "db/beacon.db", Path: dbPath}
	sec := writeSource(t, dir, "secrets/root-key", fixtureSecret)
	out := filepath.Join(dir, ArchiveName("srv", time.Now().UTC(), KindFull))
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(f, []age.Recipient{id.Recipient()}, signer, []Source{src, sec},
		Manifest{Kind: KindFull, Created: time.Now().UTC(), AuditHead: audit.Head, AuditSeq: audit.LastSeq}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return id.Recipient(), id, out, signer
}

// TestRestoreAfterTheServerIsGone: the data directory is gone; the recovery material and the
// archive are enough to restore, including when backups were encrypted to named recipients.
func TestRestoreAfterTheServerIsGone(t *testing.T) {
	server := t.TempDir()
	backupDir := filepath.Join(server, "backup")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	rs, generated, err := EnsureKey(backupDir, []age.Recipient{id.Recipient()})
	if err != nil || !generated || len(rs) != 1 {
		t.Fatalf("ensure: generated=%v %d %v", generated, len(rs), err)
	}
	material, err := os.ReadFile(filepath.Join(backupDir, "RECOVERY-MATERIAL.txt"))
	if err != nil {
		t.Fatal(err)
	}
	verify, err := ParseVerifyKey(material)
	if err != nil || len(verify) == 0 {
		t.Fatalf("the signing public key was not in the material: %v", err)
	}
	signer, err := EnsureSignKey(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(server, "beacon.db")
	st := fixtureStore(t, dbPath)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	audit, err := VerifyAudit(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	keep := t.TempDir()
	out := filepath.Join(keep, ArchiveName("srv", time.Now().UTC(), KindFull))
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(f, rs, signer, []Source{{Name: "db/beacon.db", Path: dbPath}},
		Manifest{Kind: KindFull, Created: time.Now().UTC(), AuditHead: audit.Head, AuditSeq: audit.LastSeq}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.RemoveAll(server); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Fatal("the server data directory was not gone")
	}
	into := filepath.Join(keep, "restored")
	if _, err := Restore(context.Background(), RestoreOpts{
		Paths: []string{out}, Identities: []age.Identity{id}, Verify: verify, Dir: into,
	}); err != nil {
		t.Fatalf("restore with only the recovery material: %v", err)
	}
	if _, err := os.Stat(filepath.Join(into, "db", "beacon.db")); err != nil {
		t.Fatal("the database was not restored")
	}
}

// TestRestoreWithGeneratedMaterialAlone: Beacon generated the age pair; the recovery
// material file is the identity and the verify key. After the data directory is gone, that
// file and the archive are enough.
func TestRestoreWithGeneratedMaterialAlone(t *testing.T) {
	server := t.TempDir()
	backupDir := filepath.Join(server, "backup")
	rs, generated, err := EnsureKey(backupDir, nil)
	if err != nil || !generated || len(rs) != 1 {
		t.Fatalf("ensure: generated=%v %d %v", generated, len(rs), err)
	}
	material, err := os.ReadFile(filepath.Join(backupDir, "RECOVERY-MATERIAL.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := ParseIdentities(material)
	if err != nil || len(ids) != 1 {
		t.Fatalf("identity from material: %d %v", len(ids), err)
	}
	verify, err := ParseVerifyKey(material)
	if err != nil || len(verify) == 0 {
		t.Fatalf("verify key from material: %v", err)
	}
	signer, err := EnsureSignKey(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(server, "beacon.db")
	st := fixtureStore(t, dbPath)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	audit, err := VerifyAudit(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	keep := t.TempDir()
	out := filepath.Join(keep, ArchiveName("srv", time.Now().UTC(), KindFull))
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(f, rs, signer, []Source{{Name: "db/beacon.db", Path: dbPath}},
		Manifest{Kind: KindFull, Created: time.Now().UTC(), AuditHead: audit.Head, AuditSeq: audit.LastSeq}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.RemoveAll(server); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(keep, "restored")
	if _, err := Restore(context.Background(), RestoreOpts{
		Paths: []string{out}, Identities: ids, Verify: verify, Dir: into,
	}); err != nil {
		t.Fatalf("restore with only the recovery material: %v", err)
	}
	if _, err := os.Stat(filepath.Join(into, "db", "beacon.db")); err != nil {
		t.Fatal("the database was not restored")
	}
}
