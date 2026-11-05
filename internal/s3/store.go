package s3

// Store is the recordings' Files interface (internal/sshgate) over one bucket: what a
// primary/standby pair shares, so the standby replays what the primary recorded. A recording is
// written to local disk as it is recorded — a crash then leaves at least what this instance has
// — and uploaded when the session closes it, with retries. A transcript is written locally and
// uploaded in one piece.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store implements sshgate.Files over the bucket, with a local staging directory.
type Store struct {
	c   *Client
	dir string
	log *slog.Logger
}

// NewStore keeps one client for the bucket. Dir is the local staging tree (the recordings
// directory under data_dir); empty uses a directory under the process temp dir.
func NewStore(o Options) (*Store, error) {
	c, err := New(o)
	if err != nil {
		return nil, err
	}
	dir := o.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "beacon-recordings")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Store{c: c, dir: dir, log: log}, nil
}

// uploader writes one recording to disk and uploads it when it closes.
type uploader struct {
	s    *Store
	name string
	f    *os.File
	path string
	done bool
}

func (s *Store) localPath(name string) string {
	return filepath.Join(s.dir, filepath.FromSlash(name))
}

// Create implements the Files interface.
func (s *Store) Create(name string) (io.WriteCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	path := s.localPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // inside the recordings directory
	if err != nil {
		return nil, err
	}
	return &uploader{s: s, name: name, f: f, path: path}, nil
}

// WriteFile implements the Files interface.
func (s *Store) WriteFile(name string, b []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	path := s.localPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return s.putRetry(context.Background(), name, b)
}

// ReadFile implements the Files interface.
func (s *Store) ReadFile(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(s.localPath(name)); err == nil {
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.c.GetAll(ctx, name)
}

// Open implements the Files interface.
func (s *Store) Open(name string) (io.ReadCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	f, err := os.Open(s.localPath(name))
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s.c.Get(context.Background(), name)
}

// Remove implements the Files interface. The object is deleted first, so a failure leaves the
// database row (the tidy retries); a missing object is not an error.
func (s *Store) Remove(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.c.Remove(ctx, name); err != nil {
		return err
	}
	if err := os.Remove(s.localPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (u *uploader) Write(b []byte) (int, error) { return u.f.Write(b) }

// Close uploads the recording, retrying a failed put. The local file stays: a crash or a put that
// still fails leaves something to retry from.
func (u *uploader) Close() error {
	if u.done {
		return nil
	}
	u.done = true
	if err := u.f.Sync(); err != nil {
		_ = u.f.Close()
		return err
	}
	if err := u.f.Close(); err != nil {
		return err
	}
	body, err := os.ReadFile(u.path)
	if err != nil {
		return err
	}
	return u.s.putRetry(context.Background(), u.name, body)
}

func (s *Store) putRetry(ctx context.Context, name string, body []byte) error {
	var err error
	for i := 0; i < 5; i++ {
		uctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = s.c.Put(uctx, name, body)
		cancel()
		if err == nil {
			return nil
		}
		s.log.Warn("recording upload failed", "key", name, "attempt", i+1, "err", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(1<<i) * 200 * time.Millisecond):
		}
	}
	return err
}

// checkName keeps object keys plain relative paths.
func checkName(name string) error {
	if name == "" || name[0] == '/' || containsDotDot(name) || strings.Contains(name, "\\") {
		return fmt.Errorf("s3: the object key %q is not a plain relative path", name)
	}
	return nil
}

func containsDotDot(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}
