package web

// The backups page: the recovery material shown once to an owner, served as a download, and
// removed on the owner's word — after which the page has nothing to show, because the server
// has nothing left to show. A viewer gets none of it, and no token or agent can reach the page
// at all (its scope is the owner's own).

import (
	"errors"
	"net/http"
	"net/url"
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
}

func (f *fakeBackups) RecoveryMaterial() ([]byte, bool) {
	if f.removed {
		return nil, false
	}
	return f.material, true
}

func (f *fakeBackups) RemoveRecoveryMaterial() error {
	if f.removed {
		return errors.New("already gone")
	}
	f.removed = true
	return nil
}

func (f *fakeBackups) PublicKey() string { return f.public }

const fixtureMaterial = "fixture AGE-SECRET-KEY-1-not-a-real-key-recovery-material"

func TestBackupRecoveryMaterialOnce(t *testing.T) {
	e := newEnv(t, i18n.EN)
	fb := &fakeBackups{material: []byte(fixtureMaterial), public: "age1fixture-public"}
	e.srv.Backups = fb
	alice := e.owner()

	// an owner sees it once, with the download and the removal
	page := alice.get("/console/backup")
	if page.code != http.StatusOK || !strings.Contains(page.body, fixtureMaterial) ||
		!strings.Contains(page.body, `href="/console/backup/recovery.txt"`) ||
		!strings.Contains(page.body, `action="/console/backup/remove"`) {
		t.Fatalf("the page: %d %s", page.code, page.body)
	}
	// the download serves the material, and only to an owner
	file := alice.get("/console/backup/recovery.txt")
	if file.code != http.StatusOK || !strings.Contains(file.body, fixtureMaterial) ||
		!strings.Contains(file.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("the download: %d %s", file.code, file.body)
	}
	// a viewer gets none of it
	bob := e.invite(alice, "bob", "viewer")
	if p := bob.get("/console/backup"); p.code != http.StatusForbidden {
		t.Fatalf("a viewer reached the page: %d", p.code)
	}
	if p := bob.get("/console/backup/recovery.txt"); p.code != http.StatusForbidden {
		t.Fatalf("a viewer reached the download: %d", p.code)
	}

	// removal, on the owner's word; the page cannot show it again
	if a := alice.post("/console/backup/remove", url.Values{}); a.code != http.StatusSeeOther {
		t.Fatalf("remove: %d %s", a.code, a.body)
	}
	page = alice.get("/console/backup")
	if page.code != http.StatusOK || strings.Contains(page.body, fixtureMaterial) {
		t.Fatalf("the material is still shown: %d %s", page.code, page.body)
	}
	if !strings.Contains(page.body, "age1fixture-public") {
		t.Fatal("the public key is not shown in its place")
	}
	if p := alice.get("/console/backup/recovery.txt"); p.code != http.StatusSeeOther {
		t.Fatalf("the download answers after removal: %d", p.code)
	}
	if entries := e.st.AuditLog(5); len(entries) == 0 ||
		!strings.Contains(entries[0].Action, "backup.recovery_removed") {
		t.Fatalf("the removal was not audited: %+v", entries)
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
