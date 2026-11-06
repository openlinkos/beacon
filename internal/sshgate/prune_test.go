package sshgate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

type failFiles struct {
	DirFiles
	fail string
}

func (f failFiles) Remove(name string) error {
	if strings.HasSuffix(name, f.fail) {
		return errors.New("the bucket refused")
	}
	return f.DirFiles.Remove(name)
}

// TestPruneDeletesTheObjectBeforeTheRow: a failed object delete must leave the session row so the
// next tidy retries, instead of forgetting the row and leaking the object.
func TestPruneDeletesTheObjectBeforeTheRow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	g, err := New(Deps{Store: st, HostKey: signer(t), CA: signer(t), Dir: dir, Keep: time.Hour,
		Files: failFiles{DirFiles: DirFiles{Dir: dir}, fail: ".cast"}, Lang: i18n.EN, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	rec := store.SessionRecord{ID: "20261104T110000-old", Started: now.Add(-2 * time.Hour), Recording: "2026/11/04/old"}
	if err := st.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026", "11", "04")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "old.cast"), []byte("cast"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "old.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.prune()
	if _, ok := st.Session(rec.ID); !ok {
		t.Fatal("the row was deleted after the object delete failed")
	}
}
