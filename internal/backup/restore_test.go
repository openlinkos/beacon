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
	if err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("a WAL that could not be removed: %v", err)
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
	got, err := os.ReadFile(filepath.Join(live, "beacon.db"))
	if err != nil || string(got) != "live-db" {
		t.Fatalf("the live database was not rolled back: %q %v", got, err)
	}
}

func oneArchive(t *testing.T, dir string) (age.Recipient, age.Identity, string, ed25519.PrivateKey) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	signer := testSigner(t)
	st := fixtureStore(t, filepath.Join(dir, "beacon.db"))
	_ = st.Close()
	src := Source{Name: "db/beacon.db", Path: filepath.Join(dir, "beacon.db")}
	sec := writeSource(t, dir, "secrets/root-key", fixtureSecret)
	out := filepath.Join(dir, ArchiveName("srv", time.Now().UTC(), KindFull))
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(f, []age.Recipient{id.Recipient()}, signer, []Source{src, sec},
		Manifest{Kind: KindFull, Created: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return id.Recipient(), id, out, signer
}
