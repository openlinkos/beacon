package s3

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

const sample = "2026/11/04/id-1.cast"

func newSpool(t *testing.T, endpoint, dir string) *Store {
	t.Helper()
	s, err := NewStore(Options{
		Endpoint: endpoint, Bucket: "evidence", AccessKeyID: "fixture-id", SecretAccessKey: "fixture-secret", Dir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seal(t *testing.T, s *Store, name, text string) {
	t.Helper()
	w, err := s.CreateStaged(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, text); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("the evidence worker did not advance")
		var zero T
		return zero
	}
}

func TestSpoolSealsLocallyBeforeAnS3Response(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		hash := fmt.Sprintf("%x", sha256.Sum256(b))
		if r.URL.Path != "/evidence/"+sample || r.ContentLength != int64(len(b)) || r.Header.Get("x-amz-content-sha256") != hash {
			t.Errorf("unexpected signed request for sealed evidence: %s", r.URL.Path)
		}
		started <- string(b)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	st := newSpool(t, srv.URL, t.TempDir())
	done := make(chan struct{})
	go func() {
		defer close(done)
		st.RunPending(ctx, time.Hour)
	}()

	w, err := st.CreateStaged(sample)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "header\nrecorded output"); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryPending(ctx); err != nil || st.Publication(sample) != "" || len(started) != 0 {
		t.Fatalf("open capture was queued for upload: %v", err)
	}
	sealed := make(chan error, 1)
	go func() { sealed <- w.Close() }()
	if err := await(t, sealed); err != nil {
		t.Fatal(err)
	}
	if got := await(t, started); got != "header\nrecorded output" {
		t.Fatalf("recorded bytes: %q", got)
	}
	if b, err := st.ReadFileContext(ctx, sample); string(b) != "header\nrecorded output" || err != nil {
		t.Fatalf("locally sealed evidence is unavailable during PUT: %q %v", b, err)
	}
	if got := st.Publication(sample); got != "pending" {
		t.Fatalf("publication before acceptance: %q", got)
	}
	if err := st.Remove(sample); !errors.Is(err, errSpoolBusy) {
		t.Fatalf("a deletion raced the in-flight PUT: %v", err)
	}
	if err := st.RetryPending(ctx); err != nil || len(started) != 0 {
		t.Fatalf("a second simultaneous upload was started: %v", err)
	}
	close(release)
	limit := time.NewTimer(5 * time.Second)
	defer limit.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for st.Publication(sample) != "uploaded" {
		select {
		case <-poll.C:
		case <-limit.C:
			t.Fatal("S3 acceptance did not produce a receipt")
		}
	}
	stop()
	await(t, done)
	if regularFile(st.spoolPath(sample) + sealedSuffix) {
		t.Fatal("uploaded spool data was not removed")
	}
}

func TestSpoolRestartPublishesSealedAndLeavesInterruptedCapturePrivate(t *testing.T) {
	var accepts atomic.Bool
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		puts.Add(1)
		if r.URL.Path != "/evidence/"+sample {
			t.Errorf("published an unsealed or abandoned capture: %s", r.URL.Path)
		}
		if !accepts.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	st := newSpool(t, srv.URL, dir)
	seal(t, st, sample, "sealed locally")
	interrupted := "2026/11/04/id-2.cast"
	w, err := st.CreateStaged(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Abandon() }()
	if _, err := io.WriteString(w, "incomplete"); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	abandoned := "2026/11/04/id-3.json"
	aband, err := st.CreateStaged(abandoned)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(aband, "abandoned"); err != nil {
		t.Fatal(err)
	}
	if err := aband.Abandon(); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryPending(t.Context()); err == nil {
		t.Fatal("the failed PUT was not reported")
	}
	st = newSpool(t, srv.URL, dir)
	if b, err := st.ReadFile(sample); string(b) != "sealed locally" || err != nil {
		t.Fatalf("sealed evidence not readable after restart: %q %v", b, err)
	}
	if st.Publication(sample) != "pending" || st.Publication(interrupted) != "" || st.Publication(abandoned) != "" {
		t.Fatal("wrong publication states after restart")
	}
	accepts.Store(true)
	if err := st.RetryPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if puts.Load() != 2 || st.Publication(sample) != "uploaded" {
		t.Fatalf("restart did not publish only the sealed object: %d puts, %q", puts.Load(), st.Publication(sample))
	}
	st = newSpool(t, srv.URL, dir)
	if err := st.RetryPending(t.Context()); err != nil || puts.Load() != 2 || st.Publication(sample) != "uploaded" {
		t.Fatalf("receipt was not kept across a second restart: writes=%d err=%v", puts.Load(), err)
	}
}

func TestSpoolKeepsReceiptWhenLocalCleanupFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the filesystem fault fixture renames an open file")
	}
	dir := t.TempDir()
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if puts.Add(1) != 1 {
			t.Error("the acknowledged upload was submitted again")
		}
		// Force the unlink of this pathname to fail even under a privileged test user.
		path := filepath.Join(dir, spoolDir, filepath.FromSlash(sample)) + sealedSuffix
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filepath.Join(path, "blocked"), nil, 0o600); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	st := newSpool(t, srv.URL, dir)
	seal(t, st, sample, "sealed locally")
	err := st.RetryPending(t.Context())
	if err == nil || st.Publication(sample) != "uploaded" {
		t.Fatalf("a cleanup error masked remote acceptance: %v, state=%s", err, st.Publication(sample))
	}
	path := st.spoolPath(sample) + sealedSuffix
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	st = newSpool(t, srv.URL, dir)
	if err := st.RetryPending(t.Context()); err != nil || puts.Load() != 1 || regularFile(path) {
		t.Fatalf("retry should only clean an already uploaded file: %v, %d PUTs", err, puts.Load())
	}
}

func TestSpoolRemovalDoesNotRequeueOnFailedRemoteDelete(t *testing.T) {
	var puts, deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			puts.Add(1)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if deletes.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	st := newSpool(t, srv.URL, t.TempDir())
	seal(t, st, sample, "retention reached")
	if err := st.Remove(sample); err == nil {
		t.Fatal("the failed DELETE was not reported")
	}
	st = newSpool(t, srv.URL, st.dir)
	if err := st.RetryPending(t.Context()); err != nil || puts.Load() != 0 {
		t.Fatalf("a pending retention delete requeued an upload: %v %d", err, puts.Load())
	}
	if err := st.Remove(sample); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.spoolPath(sample) + discardedSuffix); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("discarded evidence survived successful retention removal: %v", err)
	}
}

func TestSpoolStartupSkipsExpiredSealedEvidence(t *testing.T) {
	requests := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			requests <- r.URL.Path
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	primary := newSpool(t, srv.URL, dir)
	const expired = "2026/11/04/id-0.cast"
	seal(t, primary, expired, "expired")
	seal(t, primary, sample, "within retention")
	age := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(primary.spoolPath(expired)+sealedSuffix, age, age); err != nil {
		t.Fatal(err)
	}
	standby := newSpool(t, srv.URL, dir)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		standby.RunPending(ctx, time.Hour)
	}()
	if got := await(t, requests); got != "/evidence/"+sample {
		t.Fatalf("the first PUT was not for a retained object: %q", got)
	}
	stop()
	await(t, done)
	if len(requests) != 0 || standby.Publication(expired) != "" {
		t.Fatal("expired evidence remained queued after startup")
	}
}

func TestSpoolTidyKeepsDirectoriesReservedForANewCapture(t *testing.T) {
	st := &Store{dir: t.TempDir()}
	// Hold the same reservation as CreateStaged when the parent directories exist and the
	// capture file is about to be opened.
	if !st.claim(sample) {
		t.Fatal("could not reserve the capture name")
	}
	dir := filepath.Dir(st.spoolPath(sample))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st.TidyLocal(time.Hour)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("retention removed the new capture's parent directory: %v", err)
	}
	st.unmarkBusy(sample)
	st.TidyLocal(time.Hour)
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the idle spool did not clear the empty directory: %v", err)
	}
}
