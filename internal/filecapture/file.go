// Package filecapture keeps evidence private until its local write is durably sealed.
package filecapture

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Capture is a staged writer whose caller controls local durability and publication.
type Capture interface {
	io.WriteCloser
	Sync() error
	Abandon() error
}

// Writer owns a private staging file. Close seals it; Abandon releases it without publishing.
type Writer struct {
	file           *os.File
	active, sealed string
	root           string
	result         error
}

// Create reserves active for a new capture. active and sealed must be siblings under root.
func Create(root, active, sealed string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(active), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(sealed); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return nil, err
		}
		return nil, fs.ErrExist
	}
	f, err := os.OpenFile(active, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // under the caller's private storage root
	if err != nil {
		return nil, err
	}
	return &Writer{file: f, active: active, sealed: sealed, root: filepath.Clean(root)}, nil
}

// Write appends to the capture.
func (w *Writer) Write(p []byte) (int, error) {
	if w.file == nil {
		return 0, fs.ErrClosed
	}
	return w.file.Write(p)
}

// Sync makes the evidence written so far durable before the remote operation starts.
func (w *Writer) Sync() error {
	if w.file == nil {
		return fs.ErrClosed
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	// Persist directories that may have been created with the capture, including the root's entry.
	for dir := filepath.Dir(w.active); ; dir = filepath.Dir(dir) {
		if err := SyncDir(dir); err != nil {
			return err
		}
		if dir == filepath.Dir(w.root) || dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// Close makes sealed visible in one rename after flushing the file.
func (w *Writer) Close() error {
	if w.file == nil {
		return w.result
	}
	w.result = w.seal()
	w.file = nil
	if w.result != nil {
		_ = os.Remove(w.active)
	}
	return w.result
}

func (w *Writer) seal() error {
	syncErr := w.Sync()
	if err := errors.Join(syncErr, w.file.Close()); err != nil {
		return err
	}
	if err := os.Rename(w.active, w.sealed); err != nil {
		return err
	}
	if err := SyncDir(filepath.Dir(w.sealed)); err != nil {
		return errors.Join(err, os.Remove(w.sealed))
	}
	return nil
}

// Abandon closes and removes private bytes without sealing them.
func (w *Writer) Abandon() error {
	if w.file == nil {
		return w.result
	}
	w.result = errors.Join(w.file.Close(), os.Remove(w.active))
	w.file = nil
	return w.result
}

// SyncDir persists a directory's entries. Windows does not support Sync on directory handles.
func SyncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // a parent under the caller's private storage tree
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
