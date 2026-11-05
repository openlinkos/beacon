package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/store"
)

const (
	rotatePassOld = "fixture-passphrase-hush-puppy"
	rotatePassNew = "fixture-passphrase-next-hound"
)

// TestRotateRootKeyCrashEachStep injects a crash after the staged write, after the database
// commit (before rename), and after the rename (before the in-progress mark is cleared), in
// file, passphrase and KMS modes. After reopen the vault opens, settings still hold the value
// that was set, and every secret still Reveals.
func TestRotateRootKeyCrashEachStep(t *testing.T) {
	defer UseCheapPassphraseForTest()()
	for _, mode := range []string{SealFile, SealPassphrase, SealTransit} {
		for _, step := range []string{"after-stage", "after-commit", "after-rename"} {
			t.Run(mode+"/"+step, func(t *testing.T) {
				h := newRotateHarness(t, mode)
				h.wireCrash(step)
				if err := h.s.RotateRootKey(rotatePassNew); err == nil {
					t.Fatal("a crash inject must stop the rotation")
				}
				h.reopen(t, step)
				h.assertOpen(t)
			})
		}
	}
}

// TestRotateRootKeyConcurrentRefuses is two RotateRootKey calls at once: one succeeds, the
// other refuses immediately; after reopen the vault opens and secrets read.
func TestRotateRootKeyConcurrentRefuses(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.afterStageFn = func() error {
		once.Do(func() { close(started) })
		<-release
		return nil
	}
	errc := make(chan error, 1)
	go func() { errc <- s.RotateRootKey("") }()
	<-started
	if err := s.RotateRootKey(""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second rotation must refuse at once, got %v", err)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatalf("the first rotation: %v", err)
	}
	values, _, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("secrets still read after the concurrent refuse: %v", err)
	}
	s2 := reopenVault(t, s, st)
	values, _, err = s2.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("after reopen secrets still read: %v", err)
	}
}

// TestRotateRootKeyKMSOpenDoesNotDropStagedBecauseOldWrapDecrypts leaves dest as the old wrap
// and staged as the new wrap after the database commit. Open must take the new key (the key
// check, not “the old wrap still decrypts”) and keep the settings.
func TestRotateRootKeyKMSOpenDoesNotDropStagedBecauseOldWrapDecrypts(t *testing.T) {
	h := newRotateHarness(t, SealTransit)
	dest := filepath.Join(h.secretsDir, fileRKWrapped)
	oldWrap := mustRead(t, dest)
	h.s.afterCommitFn = func() error { return errors.New("crash after commit") }
	if err := h.s.RotateRootKey(""); err == nil {
		t.Fatal("the crash inject must stop the rotation")
	}
	if _, err := os.Stat(dest + ".new"); err != nil {
		t.Fatalf("staged wrap is still there: %v", err)
	}
	if got := mustRead(t, dest); string(got) != string(oldWrap) {
		t.Fatal("dest is still the old wrap")
	}
	h.reopen(t, "after-commit")
	h.assertOpen(t)
	if _, err := os.Stat(dest + ".new"); !os.IsNotExist(err) {
		t.Fatal("open took the staged wrap and renamed it")
	}
}

type rotateHarness struct {
	t          *testing.T
	mode       string
	dbPath     string
	secretsDir string
	seal       SealConfig
	http       *http.Client
	s          *Service
	st         *store.Store
	sset       *settings.Service
	item       Item
}

func newRotateHarness(t *testing.T, mode string) *rotateHarness {
	t.Helper()
	dir := t.TempDir()
	h := &rotateHarness{
		t: t, mode: mode,
		dbPath:     filepath.Join(dir, "beacon.db"),
		secretsDir: filepath.Join(dir, "secrets"),
		seal:       SealConfig{Mode: mode, Dir: filepath.Join(dir, "secrets")},
	}
	if mode == SealTransit {
		server, _ := fakeKeepers(t, &h.seal, dir)
		t.Cleanup(server.Close)
		h.http = server.Client()
	}
	h.open(t)
	if mode == SealPassphrase {
		if err := h.s.InitPassphrase(rotatePassOld); err != nil {
			t.Fatal(err)
		}
		if err := h.sset.SetKey(h.s.SK()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.sset.Set(context.Background(), settings.Actor{Name: "owner", Via: "test"}, settings.ScopeSystem, "", "request.max_uses", json.RawMessage("5"), "tighten the uses", 0); err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, h.s, "Infra")
	h.item = putItem(t, h.s, col.Name, "", "password", fixturePassword)
	return h
}

func (h *rotateHarness) open(t *testing.T) {
	t.Helper()
	st, err := store.Open(h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sset, err := settings.Open(settings.Deps{Store: st, Lang: i18n.EN, Log: testLog(t), EpochsDir: h.secretsDir})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{Store: st, Settings: sset, Lang: i18n.EN, Log: testLog(t),
		CheckPassword: func(string, string) error { return nil }, HTTP: h.http}
	s, err := Open(context.Background(), h.seal, d)
	if err != nil {
		t.Fatal(err)
	}
	h.st, h.sset, h.s = st, sset, s
	t.Cleanup(func() { _ = st.Close() })
	if !s.Sealed() {
		if err := sset.SetKey(s.SK()); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *rotateHarness) wireCrash(step string) {
	switch step {
	case "after-stage":
		h.s.afterStageFn = func() error { return errors.New("crash after staged write") }
	case "after-commit":
		h.s.afterCommitFn = func() error { return errors.New("crash after commit") }
	case "after-rename":
		h.s.afterRenameFn = func() error { return errors.New("crash after rename") }
	}
}

func (h *rotateHarness) reopen(t *testing.T, step string) {
	t.Helper()
	_ = h.s.Close()
	_ = h.st.Close()
	h.open(t)
	if h.mode == SealPassphrase {
		pass := rotatePassOld
		if step != "after-stage" {
			pass = rotatePassNew
		}
		if err := h.s.Unseal(pass); err != nil {
			t.Fatalf("unseal after %s: %v", step, err)
		}
		if err := h.sset.SetKey(h.s.SK()); err != nil {
			t.Fatal(err)
		}
	}
	if h.s.Sealed() {
		t.Fatal("the vault must be open after recovery")
	}
}

func (h *rotateHarness) assertOpen(t *testing.T) {
	t.Helper()
	values, _, err := h.s.Reveal(owner, h.item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("every secret still Reveals: %v", err)
	}
	res, err := h.sset.System("request.max_uses")
	if err != nil || string(res.Value) != "5" {
		t.Fatalf("settings must still hold 5, got %s %v", res.Value, err)
	}
}
