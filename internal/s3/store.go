package s3

// Store is the recordings' Files interface (internal/sshgate) over one bucket: what a
// primary/standby pair shares, so the standby replays what the primary recorded. The live SSH
// paths use staged captures, sealing locally before a background worker publishes to S3.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store implements sshgate.Files over the bucket, with a local staging directory.
type Store struct {
	c    *Client
	dir  string
	log  *slog.Logger
	mu   sync.Mutex
	busy map[string]struct{} // captures, uploads and removals claim the same name
	pass sync.Mutex
	wake chan struct{}
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
	return &Store{c: c, dir: dir, log: log, wake: make(chan struct{}, 1)}, nil
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
	if !s.claim(name) {
		return nil, errSpoolBusy
	}
	path := s.localPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.unmarkBusy(name)
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // inside the recordings directory
	if err != nil {
		s.unmarkBusy(name)
		return nil, err
	}
	return &uploader{s: s, name: name, f: f, path: path}, nil
}

// WriteFile implements the Files interface.
func (s *Store) WriteFile(name string, b []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	if !s.claim(name) {
		return errSpoolBusy
	}
	defer s.unmarkBusy(name)
	path := s.localPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	if err := s.putRetry(context.Background(), name, b); err != nil {
		return err
	}
	return s.removeLocal(path)
}

// ReadFile implements the Files interface.
func (s *Store) ReadFile(name string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.ReadFileContext(ctx, name)
}

// Open implements the Files interface.
func (s *Store) Open(name string) (io.ReadCloser, error) {
	return s.OpenContext(context.Background(), name)
}

// ReadFileContext reads a sealed transcript from disk or the bucket.
func (s *Store) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	r, err := s.OpenContext(ctx, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// OpenContext reads local sealed evidence first, or the legacy local or remote object.
func (s *Store) OpenContext(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	base := s.localPath(name)
	for _, path := range []string{s.spoolPath(name) + sealedSuffix, base} {
		f, err := os.Open(path) //nolint:gosec // inside the recordings directory
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return s.c.Get(ctx, name)
}

// Remove implements the Files interface. A local sealed file is taken off the upload queue
// before deletion, so an interrupted prune cannot republish it after deleting the object.
func (s *Store) Remove(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if !s.claim(name) {
		return errSpoolBusy
	}
	defer s.unmarkBusy(name)
	base := s.localPath(name)
	staged := s.spoolPath(name)
	if err := quarantine(staged); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.c.Remove(ctx, name); err != nil {
		return err
	}
	return errors.Join(s.removeLocal(base), s.removeLocal(staged+activeSuffix),
		s.removeLocal(staged+discardedSuffix), s.removeLocal(base+uploadedSuffix))
}

func (u *uploader) Write(b []byte) (int, error) { return u.f.Write(b) }

// Close uploads the recording, retrying a failed put. A successful put removes the local file; a
// crash or a put that still fails leaves something to retry from.
func (u *uploader) Close() error {
	if u.done {
		return nil
	}
	u.done = true
	defer u.s.unmarkBusy(u.name)
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
	if err := u.s.putRetry(context.Background(), u.name, body); err != nil {
		return err
	}
	return u.s.removeLocal(u.path)
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

func (s *Store) claim(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.busy[name]; ok {
		return false
	}
	if s.busy == nil {
		s.busy = map[string]struct{}{}
	}
	s.busy[name] = struct{}{}
	return true
}

func (s *Store) unmarkBusy(name string) {
	s.mu.Lock()
	delete(s.busy, name)
	s.mu.Unlock()
}

func (s *Store) removeLocal(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// objectName is the slash-separated key for a path under the staging directory; false if it is not
// a plain relative path inside the tree.
func objectName(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", false
	}
	name := filepath.ToSlash(rel)
	if checkName(name) != nil {
		return "", false
	}
	return name, true
}

// TidyLocal removes leftover staging files whose modtime is older than keep. keep <= 0 skips.
// Sealed evidence stays until the session's own retention removal.
func (s *Store) TidyLocal(keep time.Duration) {
	if keep <= 0 {
		return
	}
	cutoff := time.Now().Add(-keep)
	s.tidyLocalTree(s.dir, cutoff)
	s.tidyLocalTree(s.spoolRoot(), cutoff)
}

func (s *Store) tidyLocalTree(root string, cutoff time.Time) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == s.spoolRoot() && root != path {
				return filepath.SkipDir
			}
			if path != root {
				dirs = append(dirs, path)
			}
			return nil
		}
		base := strings.TrimSuffix(strings.TrimSuffix(path, activeSuffix), discardedSuffix)
		name, ok := objectName(root, base)
		if !ok || !s.claim(name) {
			return nil
		}
		defer s.unmarkBusy(name)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			_ = s.removeLocal(path)
		}
		return nil
	})
	// A reservation protects the gap between creating the parent directories and opening the
	// capture file. Keep it exclusive with directory removal because sibling keys share parents.
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.busy) != 0 {
		return
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
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
