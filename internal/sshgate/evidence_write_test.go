package sshgate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

var errFixtureEvidence = errors.New("private fixture storage failure")

type evidenceFaultFiles struct {
	DirFiles
	failCreate  bool
	castWrite   int
	shortWrite  bool
	failSync    bool
	failClose   bool
	failSidecar bool
	beforeSync  func()
}

func (f evidenceFaultFiles) CreateStaged(name string) (captureWriter, error) {
	if f.failCreate || f.failSidecar && strings.HasSuffix(name, ".json") {
		return nil, errFixtureEvidence
	}
	w, err := f.DirFiles.CreateStaged(name)
	if err != nil || !strings.HasSuffix(name, ".cast") {
		return w, err
	}
	return &evidenceFaultWriter{
		captureWriter: w, failWrite: f.castWrite, shortWrite: f.shortWrite,
		failSync: f.failSync, failClose: f.failClose, beforeSync: f.beforeSync,
	}, nil
}

type evidenceFaultWriter struct {
	captureWriter
	writes, failWrite int
	shortWrite        bool
	failSync          bool
	failClose         bool
	beforeSync        func()
}

func (w *evidenceFaultWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes != w.failWrite {
		return w.captureWriter.Write(p)
	}
	if w.shortWrite {
		return w.captureWriter.Write(p[:len(p)/2])
	}
	return 0, errFixtureEvidence
}

func (w *evidenceFaultWriter) Sync() error {
	if w.beforeSync != nil {
		w.beforeSync()
	}
	if w.failSync {
		return errFixtureEvidence
	}
	return w.captureWriter.Sync()
}

func (w *evidenceFaultWriter) Close() error {
	if w.failClose {
		_ = w.Abandon()
		return errFixtureEvidence
	}
	return w.captureWriter.Close()
}

func TestSSHEvidenceAdmissionRefusesUnrecordableOperations(t *testing.T) {
	needShell(t)
	for _, tc := range []struct {
		name  string
		fault evidenceFaultFiles
	}{
		{"creation", evidenceFaultFiles{failCreate: true}},
		{"recording header", evidenceFaultFiles{castWrite: 1}},
		{"header short write", evidenceFaultFiles{castWrite: 1, shortWrite: true}},
		{"header sync", evidenceFaultFiles{failSync: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			tc.fault.Dir = e.dir
			e.g.d.Files = tc.fault
			ctx := context.Background()
			who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
			marker := filepath.ToSlash(filepath.Join(e.tg.home, "remote-marker"))
			cmd := "touch " + quote(marker)
			if _, err := e.g.Run(ctx, who, "box", RunInput{Command: cmd}); !errors.Is(err, ErrEvidenceUnavailable) {
				t.Fatalf("run could not identify its admission failure: %v", err)
			}
			if _, err := e.g.WriteFile(ctx, who, "box", marker, "sent"); !errors.Is(err, ErrEvidenceUnavailable) {
				t.Fatalf("a file write bypassed preflight: %v", err)
			}
			if _, stderr, code := e.run(t, "box", cmd); code != 1 || !strings.Contains(stderr, ErrEvidenceUnavailable.Error()) || strings.Contains(stderr, errFixtureEvidence.Error()) {
				t.Fatalf("raw SSH admission reported exit %d, stderr %q", code, stderr)
			}
			if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the remote command or file tool modified the target: %v", err)
			}

			cl, err := e.client(t, "box", e.agentKey)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cl.Close() }()
			if conn, err := cl.Dial("tcp", "127.0.0.1:1234"); err == nil {
				_ = conn.Close()
				t.Fatal("an unrecordable forward was allowed")
			} else if !strings.Contains(err.Error(), ErrEvidenceUnavailable.Error()) {
				t.Fatalf("the forward got past evidence admission: %v", err)
			}
			if e.tg.forwardRequests.Load() != 0 {
				t.Fatal("the forwarded connection reached the target")
			}
			if got := e.st.Sessions(10, nil); len(got) != 0 {
				t.Fatalf("an operation was indexed as started without a valid header: %+v", got)
			}
		})
	}
}

func TestSSHEvidenceAdmissionRequiresInitialSessionIndex(t *testing.T) {
	needShell(t)
	e := setup(t)
	e.g.d.Files = evidenceFaultFiles{DirFiles: DirFiles{Dir: e.dir}, beforeSync: func() {
		// The request has already resolved authority and dialled, but has not run the command.
		_ = e.st.Close()
	}}
	marker := filepath.ToSlash(filepath.Join(e.tg.home, "remote-marker"))
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	if _, err := e.g.Run(context.Background(), who, "box", RunInput{Command: "touch " + quote(marker)}); !errors.Is(err, ErrEvidenceUnavailable) {
		t.Fatalf("a command with no durable index did not fail preflight: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a remote side effect occurred after initial index failure: %v", err)
	}
	if err := fs.WalkDir(os.DirFS(e.dir), ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("index failure left an evidence artifact: %s", name)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSSHFinalisationPreservesRemoteResultAndIndependentArtifacts(t *testing.T) {
	needShell(t)
	for _, tc := range []struct {
		name       string
		fault      evidenceFaultFiles
		recording  store.EvidenceState
		transcript store.EvidenceState
	}{
		{"healthy", evidenceFaultFiles{}, store.EvidenceComplete, store.EvidenceComplete},
		{"recording close", evidenceFaultFiles{failClose: true}, store.EvidenceUnavailable, store.EvidenceComplete},
		{"sidecar", evidenceFaultFiles{failSidecar: true}, store.EvidenceComplete, store.EvidenceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			tc.fault.Dir = e.dir
			e.g.d.Files = tc.fault
			who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
			run, err := e.g.Run(context.Background(), who, "box", RunInput{Command: "printf retained; exit 7"})
			if err != nil || run.ExitCode != 7 || run.Stdout != "retained" || run.RecordingState != tc.recording || run.TranscriptState != tc.transcript {
				t.Fatalf("remote result or evidence was misreported: %+v, %v", run, err)
			}
			r, ok := e.st.Session(run.Session)
			if !ok || r.Exit == nil || *r.Exit != 7 || r.Ended.IsZero() ||
				r.RecordingState != tc.recording || r.TranscriptState != tc.transcript {
				t.Fatalf("final states did not persist: %+v", r)
			}
			if _, err := os.Stat(filepath.Join(e.dir, r.Recording+".cast")); (err == nil) != (tc.recording == store.EvidenceComplete) {
				t.Fatalf("cast publication disagrees with its state: %v", err)
			}
			if b, err := os.ReadFile(filepath.Join(e.dir, r.Recording+".json")); (err == nil) != (tc.transcript == store.EvidenceComplete) {
				t.Fatalf("sidecar publication disagrees with its state: %v", err)
			} else if err == nil && !bytes.Contains(b, []byte("retained")) {
				t.Fatal("the surviving transcript lost output")
			}
		})
	}
}

// TestSSHRecordingFailureMidSessionStopsATool: a recording whose file fails while a tool's command runs
// takes nothing more, so what the machine sends next does not reach the agent: the call is stopped and
// says so, naming the session, and the session is marked as closed for it.
func TestSSHRecordingFailureMidSessionStopsATool(t *testing.T) {
	needShell(t)
	for _, tc := range []struct {
		name  string
		fault evidenceFaultFiles
	}{
		{"failed write", evidenceFaultFiles{castWrite: 2}},
		{"short write", evidenceFaultFiles{castWrite: 2, shortWrite: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			tc.fault.Dir = e.dir
			e.g.d.Files = tc.fault
			run, err := e.g.Run(context.Background(), toolActor, "box", RunInput{Command: "printf un%s recorded; exit 7"})
			if !errors.Is(err, ErrRecordingFailed) || run.Stdout != "" {
				t.Fatalf("the call went on without its recording: %+v, %v", run, err)
			}
			recs := waitSessions(t, e.st, 1)
			r := recs[0]
			if r.Stopped != store.StoppedRecordingFailed || r.RecordingState != store.EvidenceUnavailable || r.Ended.IsZero() {
				t.Fatalf("record %+v", r)
			}
			if !strings.Contains(err.Error(), r.ID) {
				t.Errorf("the error does not name the session: %v", err)
			}
			if _, err := os.Stat(filepath.Join(e.dir, r.Recording+".cast")); err == nil {
				t.Fatal("a failed recording was published")
			}
			if b, err := os.ReadFile(filepath.Join(e.dir, r.Recording+".json")); err == nil && bytes.Contains(b, []byte("unrecorded")) {
				t.Fatal("the transcript holds output the recording did not take")
			}
		})
	}
}

// TestSSHRecordingFailureMidSessionClosesTheConnection: over the agent's own ssh, input that the recording
// could not take does not reach the machine; the session is closed and the agent told why.
func TestSSHRecordingFailureMidSessionClosesTheConnection(t *testing.T) {
	needShell(t)
	e := setup(t)
	e.g.d.Files = evidenceFaultFiles{DirFiles: DirFiles{Dir: e.dir}, castWrite: 2}
	marker := filepath.Join(e.tg.home, "unrecorded-input")
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	s.Stdout, s.Stderr = io.Discard, &stderr
	s.Stdin = strings.NewReader("touch " + quote(marker) + "\n")
	err = s.Run("sh")
	var ee *ssh.ExitError
	if err == nil || errors.As(err, &ee) && ee.ExitStatus() == 0 {
		t.Fatalf("the session went on without its recording: %v", err)
	}
	if !strings.Contains(stderr.String(), ErrRecordingFailed.Error()) {
		t.Errorf("the agent was not told why: %q", stderr.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("input the recording did not take reached the machine: %v", err)
	}
	recs := waitSessions(t, e.st, 1)
	if recs[0].Stopped != store.StoppedRecordingFailed || recs[0].RecordingState != store.EvidenceUnavailable {
		t.Fatalf("record %+v", recs[0])
	}
}

func TestSSHEvidenceDistinguishesCastAndTranscriptCaps(t *testing.T) {
	for _, cap := range []string{"cast", "text"} {
		t.Run(cap, func(t *testing.T) {
			e := setup(t)
			who := Actor{Name: "claude-code", Via: "ssh"}
			m, _ := e.st.Machine("box")
			s := e.g.newSession(who, m, newID(time.Now())+"-1")
			if err := s.start("exec", "echo evidence"); err != nil {
				t.Fatal(err)
			}
			wantCast, wantText := store.EvidenceComplete, store.EvidenceComplete
			if cap == "cast" {
				s.cast.used = maxRecording
				s.output([]byte("the transcript can retain this"), false)
				wantCast = store.EvidenceTruncated
			} else {
				s.output(bytes.Repeat([]byte("a"), maxText+1), false)
				wantText = store.EvidenceTruncated
			}
			txn := s.finish(nil, true)
			if txn.RecordingState != wantCast || txn.TranscriptState != wantText ||
				!txn.Truncated || txn.textTruncated != (wantText == store.EvidenceTruncated) {
				t.Fatalf("capture limits were conflated: recording=%s transcript=%s text_cut=%v",
					txn.RecordingState, txn.TranscriptState, txn.Truncated)
			}
		})
	}
}

func TestSSHEvidenceRecorderClosesOnceAfterConcurrentEvents(t *testing.T) {
	files := DirFiles{Dir: t.TempDir()}
	const name = "nested/race.cast"
	w, err := files.CreateStaged(name)
	if err != nil {
		t.Fatal(err)
	}
	fault := &evidenceFaultWriter{captureWriter: w, failWrite: 2, shortWrite: true}
	r, err := newRecorder(fault, 80, 24, "echo test", "test session", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	r.event("o", "partial record")
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { r.event("i", "ignored after first failure") })
		wg.Go(func() {
			if err := r.close(); !errors.Is(err, io.ErrShortWrite) {
				t.Errorf("close dropped the first capture failure: %v", err)
			}
		})
	}
	wg.Wait()
	if _, err := files.Open(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("malformed cast was published: %v", err)
	}
}

func TestSSHEvidenceReportsMetadataOnlyModes(t *testing.T) {
	for _, kind := range []string{"shell", "sftp", "forward"} {
		t.Run(kind, func(t *testing.T) {
			e := setup(t)
			m, _ := e.st.Machine("box")
			s := e.g.newSession(Actor{Name: "claude-code", Via: "ssh"}, m, newID(time.Now())+"-1")
			if err := s.start(kind, ""); err != nil {
				t.Fatal(err)
			}
			s.input([]byte("ordinary input"))
			s.output([]byte("ordinary output"), false)
			tr := s.finish(nil, true)
			wantCast := store.EvidenceMetadataOnly
			if kind == "shell" {
				wantCast = store.EvidenceComplete // The shell stream is known; its structured commands are not.
			}
			if tr.RecordingState != wantCast || tr.TranscriptState != store.EvidenceMetadataOnly ||
				tr.BytesIn == 0 || tr.BytesOut == 0 || len(tr.Commands) != 0 || tr.Stdout != "" {
				t.Fatalf("metadata-only coverage was misstated: %+v", tr)
			}
			if _, err := os.Stat(filepath.Join(e.dir, tr.Recording+".json")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("metadata-only mode published a pretend transcript: %v", err)
			}
		})
	}
}

func TestSSHEvidenceOnlyAdmitsOneOperationPerChannel(t *testing.T) {
	needShell(t)
	e := setup(t)
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	ch, req, err := cl.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()
	go ssh.DiscardRequests(req)
	command := func(s string) []byte { return ssh.Marshal(struct{ Command string }{s}) }
	if ok, err := ch.SendRequest("exec", true, command("read line; printf '%s' \"$line\"")); err != nil || !ok {
		t.Fatalf("first operation was not admitted: %v, %v", ok, err)
	}
	response := make(chan error, 1)
	go func() {
		ok, err := ch.SendRequest("exec", true, command("exit 9"))
		if ok {
			err = errors.New("second remote operation was accepted")
		}
		response <- err
	}()
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second execution was not rejected while the first awaited input")
	}
	if _, err := io.WriteString(ch, "first operation\n"); err != nil {
		t.Fatal(err)
	}
	if err := ch.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(ch)
	if err != nil || string(out) != "first operation" {
		t.Fatalf("the rejected request affected the first operation: %q, %v", out, err)
	}
	if rows := waitSessions(t, e.st, 1); len(rows) != 1 || rows[0].Exit == nil || *rows[0].Exit != 0 {
		t.Fatalf("the operation's evidence was not closed accurately: %+v", rows)
	}
}
