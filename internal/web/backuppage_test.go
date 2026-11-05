package web

// The backups page: the recovery material shown once to an owner, served as a download that
// consumes it, and removed on the owner's word — after which the page has nothing to show,
// because the server has nothing left to show. A viewer gets none of it, a token session gets
// none of it, and no agent can reach the page at all (its scope is the owner's own).

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
)

// fakeBackups is the manager as the page needs it, over a material string that only a fixture
// would hold.
type fakeBackups struct {
	material []byte
	removed  bool
	public   string
	sign     string
}

func (f *fakeBackups) RecoveryMaterial() ([]byte, bool) {
	if f.removed || len(f.material) == 0 {
		return nil, false
	}
	return f.material, true
}

func (f *fakeBackups) ConsumeRecoveryMaterial() ([]byte, error) {
	if f.removed || len(f.material) == 0 {
		return nil, os.ErrNotExist
	}
	b := f.material
	f.removed = true
	f.material = nil
	return b, nil
}

func (f *fakeBackups) RemoveRecoveryMaterial() error {
	if f.removed {
		return errors.New("already gone")
	}
	f.removed = true
	f.material = nil
	return nil
}

func (f *fakeBackups) PublicKey() string     { return f.public }
func (f *fakeBackups) SignPublicKey() string { return f.sign }

const fixtureMaterial = "fixture AGE-SECRET-KEY-1-not-a-real-key-recovery-material"

func TestBackupRecoveryMaterialOnce(t *testing.T) {
	e := newEnv(t, i18n.EN)
	fb := &fakeBackups{material: []byte(fixtureMaterial), public: "age1fixture-public", sign: "sign1:fixture"}
	e.srv.Backups = fb
	alice := e.owner()

	page := alice.get("/console/backup")
	if page.code != http.StatusOK || !strings.Contains(page.body, fixtureMaterial) ||
		!strings.Contains(page.body, `href="/console/backup/recovery.txt"`) ||
		!strings.Contains(page.body, `action="/console/backup/remove"`) {
		t.Fatalf("the page: %d %s", page.code, page.body)
	}
	if page.header.Get("Cache-Control") != "private, no-store" && page.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the page is cacheable: %q", page.header.Get("Cache-Control"))
	}
	if entries := e.st.AuditLog(5); len(entries) == 0 ||
		!strings.Contains(entries[0].Action, "backup.recovery_viewed") {
		t.Fatalf("the view was not audited: %+v", entries)
	}

	file := alice.get("/console/backup/recovery.txt")
	if file.code != http.StatusOK || !strings.Contains(file.body, fixtureMaterial) ||
		!strings.Contains(file.header.Get("Content-Disposition"), "attachment") ||
		file.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the download: %d %s %q", file.code, file.body, file.header.Get("Cache-Control"))
	}
	if entries := e.st.AuditLog(5); len(entries) == 0 ||
		!strings.Contains(entries[0].Action, "backup.recovery_downloaded") {
		t.Fatalf("the download was not audited: %+v", entries)
	}

	page = alice.get("/console/backup")
	if page.code != http.StatusOK || strings.Contains(page.body, fixtureMaterial) {
		t.Fatalf("the material is still shown after download: %d %s", page.code, page.body)
	}
	if !strings.Contains(page.body, "age1fixture-public") || !strings.Contains(page.body, "sign1:fixture") {
		t.Fatal("the public keys are not shown in its place")
	}
	if p := alice.get("/console/backup/recovery.txt"); p.code != http.StatusSeeOther {
		t.Fatalf("the download answers after consumption: %d", p.code)
	}

	bob := e.invite(alice, "bob", "viewer")
	if p := bob.get("/console/backup"); p.code != http.StatusForbidden {
		t.Fatalf("a viewer reached the page: %d", p.code)
	}
	if p := bob.get("/console/backup/recovery.txt"); p.code != http.StatusForbidden {
		t.Fatalf("a viewer reached the download: %d", p.code)
	}
}

func TestBackupRecoveryMaterialRemoveStillWorks(t *testing.T) {
	e := newEnv(t, i18n.EN)
	fb := &fakeBackups{material: []byte(fixtureMaterial), public: "age1fixture-public"}
	e.srv.Backups = fb
	alice := e.owner()
	if a := alice.post("/console/backup/remove", url.Values{}); a.code != http.StatusSeeOther {
		t.Fatalf("remove: %d %s", a.code, a.body)
	}
	page := alice.get("/console/backup")
	if page.code != http.StatusOK || strings.Contains(page.body, fixtureMaterial) {
		t.Fatalf("the material is still shown: %d %s", page.code, page.body)
	}
	if entries := e.st.AuditLog(5); len(entries) == 0 ||
		!strings.Contains(entries[0].Action, "backup.recovery_removed") {
		t.Fatalf("the removal was not audited: %+v", entries)
	}
}

func TestBackupRecoveryMaterialRefusesATokenSession(t *testing.T) {
	e := newEnv(t, i18n.EN)
	fb := &fakeBackups{material: []byte(fixtureMaterial), public: "age1fixture-public"}
	e.srv.Backups = fb
	tok := e.signedIn("script", "status:read", "accounts:write")
	if p := tok.get("/console/backup"); p.code != http.StatusForbidden ||
		strings.Contains(p.body, fixtureMaterial) {
		t.Fatalf("a token session reached the page: %d %s", p.code, p.body)
	}
	if p := tok.get("/console/backup/recovery.txt"); p.code != http.StatusForbidden ||
		strings.Contains(p.body, fixtureMaterial) {
		t.Fatalf("a token session reached the download: %d %s", p.code, p.body)
	}
}

func TestBackupPageWhenOff(t *testing.T) {
	e := newEnv(t, i18n.EN)
	alice := e.owner()
	page := alice.get("/console/backup")
	if page.code != http.StatusOK || !strings.Contains(page.body, "Backups are off") {
		t.Fatalf("the off page: %d %s", page.code, page.body)
	}
}
