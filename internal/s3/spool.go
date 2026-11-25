package s3

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/filecapture"
)

// The legacy object-key validator refuses "..", including in these filenames. Earlier versions
// of Beacon therefore cannot publish staging files even if an operator rolls back the daemon.
const (
	spoolDir        = "..spool"
	activeSuffix    = "..capture"
	sealedSuffix    = "..sealed"
	uploadedSuffix  = "..uploaded"
	discardedSuffix = "..discarded"
	uploadTimeout   = 2 * time.Minute
)

var errSpoolBusy = errors.New("recording spool: evidence is in use")

func (s *Store) spoolRoot() string { return filepath.Join(s.dir, spoolDir) }

func (s *Store) spoolPath(name string) string {
	return filepath.Join(s.spoolRoot(), filepath.FromSlash(name))
}

type spoolCapture struct {
	*filecapture.Writer
	store *Store
	name  string
	done  bool
}

// CreateStaged starts a private local capture. Closing it publishes only to the local spool.
func (s *Store) CreateStaged(name string) (filecapture.Capture, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	if !s.claim(name) {
		return nil, errSpoolBusy
	}
	base := s.localPath(name)
	staged := s.spoolPath(name)
	for _, path := range []string{base, base + uploadedSuffix, staged + discardedSuffix} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			s.unmarkBusy(name)
			if err != nil {
				return nil, err
			}
			return nil, fs.ErrExist
		}
	}
	w, err := filecapture.Create(s.dir, staged+activeSuffix, staged+sealedSuffix)
	if err != nil {
		s.unmarkBusy(name)
		return nil, err
	}
	return &spoolCapture{Writer: w, store: s, name: name}, nil
}

func (w *spoolCapture) Close() error {
	err := w.Writer.Close()
	w.finish(err == nil)
	return err
}

func (w *spoolCapture) Abandon() error {
	err := w.Writer.Abandon()
	w.finish(false)
	return err
}

func (w *spoolCapture) finish(sealed bool) {
	if w.done {
		return
	}
	w.done = true
	w.store.unmarkBusy(w.name)
	if sealed {
		select {
		case w.store.wake <- struct{}{}:
		default:
		}
	}
}

// Publication reports locally proven S3 publication for name. An empty value means this instance
// has neither a sealed pending copy nor a receipt, as for legacy objects or another node's uploads.
func (s *Store) Publication(name string) string {
	if checkName(name) != nil {
		return ""
	}
	base := s.localPath(name)
	if regularFile(base + uploadedSuffix) {
		return "uploaded"
	}
	if regularFile(s.spoolPath(name) + sealedSuffix) {
		return "pending"
	}
	return ""
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// RunPending keeps at most one upload in flight per process. A seal wakes it; periodic retries
// recover from a temporary outage without making every subsequent session retry the provider.
func (s *Store) RunPending(ctx context.Context, keep time.Duration) {
	retry := time.NewTicker(time.Minute)
	defer retry.Stop()
	tidy := time.NewTicker(time.Hour)
	defer tidy.Stop()
	waiting := false
	upload := func() {
		err := s.retryPending(ctx, keep)
		waiting = err != nil
		if waiting && ctx.Err() == nil {
			s.log.Warn("pending evidence could not all be published", "err", err)
		}
	}
	s.TidyLocal(keep)
	upload()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			if !waiting {
				upload()
			}
		case <-retry.C:
			upload()
		case <-tidy.C:
			s.TidyLocal(keep)
		}
	}
}

// RetryPending publishes only locally sealed evidence. Bare legacy files remain readable locally;
// their presence cannot establish that recording or transcript capture finished before a crash.
func (s *Store) RetryPending(ctx context.Context) error {
	return s.retryPending(ctx, 0)
}

func (s *Store) retryPending(ctx context.Context, keep time.Duration) error {
	if !s.pass.TryLock() {
		return nil
	}
	defer s.pass.Unlock()
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	cutoff := time.Now().Add(-keep)
	var firstErr error
	root := s.spoolRoot()
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(path, sealedSuffix) {
			return nil
		}
		name, ok := objectName(root, strings.TrimSuffix(path, sealedSuffix))
		if !ok || !s.claim(name) {
			return nil
		}
		defer s.unmarkBusy(name)
		if err := s.publishIfRetained(ctx, name, path, keep > 0, cutoff); err != nil && firstErr == nil {
			firstErr = err
		}
		return nil
	})
	return errors.Join(firstErr, walkErr)
}

func (s *Store) publishIfRetained(ctx context.Context, name, sealed string, prune bool, cutoff time.Time) error {
	info, err := os.Lstat(sealed)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // removed between the scan and the claim
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	// Apply the local retention cut-off before every PUT, including a standby's first scan.
	// The primary separately removes expired session rows and their remote objects.
	if prune && info.ModTime().Before(cutoff) {
		return s.removeLocal(sealed)
	}
	marker := s.localPath(name) + uploadedSuffix
	if !regularFile(marker) {
		if err := s.c.putFile(ctx, name, sealed); err != nil {
			return fmt.Errorf("publish evidence %s: %w", name, err)
		}
		// Persist the remote receipt before removing the cached bytes. A cleanup error keeps
		// the acknowledgement intact and is retried without submitting the object again.
		if err := s.recordUpload(marker); err != nil {
			return fmt.Errorf("record S3 acceptance for %s: %w", name, err)
		}
	}
	if err := s.removeLocal(sealed); err != nil {
		return fmt.Errorf("clean the local spool after uploading %s: %w", name, err)
	}
	return nil
}

func (s *Store) recordUpload(marker string) error {
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // inside the recordings directory
	if err != nil {
		return err
	}
	return errors.Join(f.Close(), filecapture.SyncParents(s.dir, marker))
}

func quarantine(base string) error {
	if err := os.Rename(base+sealedSuffix, base+discardedSuffix); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return filecapture.SyncDir(filepath.Dir(base))
}
