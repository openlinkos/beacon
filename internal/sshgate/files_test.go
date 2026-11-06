package sshgate

// The recordings' Files interface over the data directory's tree: files land where the sessions'
// names say, the day's directories come and go, and names that leave the tree are refused.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirFiles(t *testing.T) {
	dir := t.TempDir()
	f := DirFiles{Dir: dir}

	w, err := f.Create("2026/11/04/id-1.cast")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("asciicast")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// a file of the same name is not expected to exist: the session's id names it
	if _, err := f.Create("2026/11/04/id-1.cast"); err == nil {
		t.Error("creating over an existing recording should fail, as the directory's own did")
	}
	if err := f.WriteFile("2026/11/04/id-1.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	b, err := f.ReadFile("2026/11/04/id-1.json")
	if err != nil || !bytes.Equal(b, []byte("{}")) {
		t.Fatalf("transcript: %q %v", b, err)
	}
	if r, err := f.Open("2026/11/04/id-1.cast"); err != nil {
		t.Fatal(err)
	} else if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("2026/11/04/id-1.cast"); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("2026/11/04/id-1.cast"); err != nil {
		t.Fatal("removing what is not there is no error")
	}
	if err := f.Remove("2026/11/04/id-1.json"); err != nil {
		t.Fatal(err)
	}
	// the day's directory went with the last file of the day
	if _, err := os.Stat(filepath.Join(dir, "2026", "11", "04")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the day's directory stayed behind")
	}
	for _, bad := range []string{"", "/abs", "../escape", `back\slash`} {
		if _, err := f.Create(bad); err == nil {
			t.Errorf("the name %q should be refused", bad)
		}
	}
	// the recordings directory itself is where the service always kept it
	if !strings.HasPrefix(filepath.ToSlash(filepath.Join(dir, "x")), filepath.ToSlash(dir)) {
		t.Fatal("unreachable")
	}
}
