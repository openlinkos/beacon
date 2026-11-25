package filecapture

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFailureKeepsTheCaptureUnpublished(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	sealed := filepath.Join(root, "sealed")
	w, err := Create(root, active, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("early data")); err != nil {
		t.Fatal(err)
	}
	if err := w.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("interrupted frame")); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("write after local failure: %v", err)
	}
	if err := w.Close(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("a failed write was sealed: %v", err)
	}
	if _, err := os.Stat(sealed); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("incomplete bytes became visible: %v", err)
	}
}
