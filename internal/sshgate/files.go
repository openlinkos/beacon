package sshgate

// Files is where the sessions' recordings and transcripts are kept: the recordings directory under
// the data directory by default, or an S3-compatible bucket an HA pair shares (a standby replays
// what the primary recorded; both read and write the same bucket). Names are slash-separated
// relative paths — a day's directory, then the session: 2026/11/04/<id>.cast and .json beside it.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Files is one recordings store. A file that is not there reads as an error naming it; removing
// one that is not there is no error.
type Files interface {
	// Create makes the file and returns its writer. A recording's name is its session's id, so a
	// file of that name is not expected to exist.
	Create(name string) (io.WriteCloser, error)
	// WriteFile stores b under the name in one piece (a transcript).
	WriteFile(name string, b []byte) error
	// ReadFile returns what WriteFile stored.
	ReadFile(name string) ([]byte, error)
	// Open streams what is under the name (a recording's replay).
	Open(name string) (io.ReadCloser, error)
	// Remove deletes the file.
	Remove(name string) error
}

// DirFiles keeps recordings under one directory, the default (the data directory's recordings
// tree). The day's directories are made as needed, and removed when a prune empties them.
type DirFiles struct {
	Dir string
}

// checkName keeps names relative and inside the tree.
func checkName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return errors.New("not a plain relative path")
	}
	return nil
}

// Create implements Files.
func (d DirFiles) Create(name string) (io.WriteCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(d.Dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // inside the recordings directory
}

// WriteFile implements Files.
func (d DirFiles) WriteFile(name string, b []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	path := filepath.Join(d.Dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// ReadFile implements Files.
func (d DirFiles) ReadFile(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(d.Dir, filepath.FromSlash(name)))
}

// Open implements Files.
func (d DirFiles) Open(name string) (io.ReadCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return os.Open(filepath.Join(d.Dir, filepath.FromSlash(name)))
}

// Remove implements Files, dropping the day's directories once they are empty.
func (d DirFiles) Remove(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	path := filepath.Join(d.Dir, filepath.FromSlash(name))
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil // removing what is not there is no error, as prune treats it
	}
	dropEmptyDirs(filepath.Dir(path), d.Dir)
	return nil
}

// dropEmptyDirs removes the day's directory, and the month's and year's above it, once empty.
func dropEmptyDirs(dir, root string) {
	for ; strings.HasPrefix(dir, filepath.Clean(root)+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return // not empty, or gone: the day goes on
		}
	}
}
