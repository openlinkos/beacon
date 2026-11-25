package sshgate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

func TestSessionEvidenceReadsIndependently(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	files := &readCountingFiles{Files: DirFiles{Dir: t.TempDir()}}
	g := &Gate{d: Deps{Store: st, Files: files}}
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	m, err := st.RegisterMachine(store.Machine{Name: "east", Added: now.Add(-time.Hour)}, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	exit := 7
	r := store.SessionRecord{
		ID: "20261125-100000-abcdef-1", Machine: m.Name, MachineRegistration: m.Registration(), Kind: "exec",
		Started: now, Ended: now.Add(time.Second), Exit: &exit, Recording: "2026/11/25/20261125-100000-abcdef-1",
		RecordingState: store.EvidenceComplete, TranscriptState: store.EvidenceComplete,
	}
	if err := st.PutSession(r); err != nil {
		t.Fatal(err)
	}
	cast, transcript := r.Recording+".cast", r.Recording+".json"
	validTranscript, err := json.Marshal(Transcript{SessionRecord: r, Stdout: "actual output\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteFile(transcript, validTranscript); err != nil {
		t.Fatal(err)
	}
	allowed := store.Principal{Scopes: []string{"sessions:read"}, Machines: []string{m.Name}}

	denied := store.Principal{Scopes: allowed.Scopes, Machines: []string{"west"}}
	if _, err := g.SessionDetailFor(t.Context(), denied, r.ID); !errors.Is(err, ErrNoSession) || files.reads != 0 {
		t.Fatalf("the forbidden detail attempted to read an artifact: %v, %d", err, files.reads)
	}
	tr, err := g.SessionDetailFor(t.Context(), allowed, r.ID)
	if err != nil || tr.RecordingAvailable == nil || *tr.RecordingAvailable ||
		tr.RecordingState != store.EvidenceComplete || tr.TranscriptState != store.EvidenceComplete ||
		tr.Stdout != "actual output\n" || tr.Exit == nil || *tr.Exit != exit {
		t.Fatalf("an unavailable replay discarded the capture or transcript: %+v, %v", tr, err)
	}
	if f, err := g.RecordingFor(allowed, r.ID); f != nil || !errors.Is(err, ErrRecordingUnavailable) {
		t.Fatalf("a visible session whose recording is unavailable returned %v, %v", f, err)
	}
	if err := files.WriteFile(cast, []byte("{\"version\":3}\n")); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"null",
		`{"stdout":"partially decoded", "commands":true}`,
		`{"id":"20261125-100000-abcdef-2", "stdout":"another session"}`,
	} {
		if err := files.WriteFile(transcript, []byte(invalid)); err != nil {
			t.Fatal(err)
		}
		tr, err := g.SessionDetailFor(t.Context(), allowed, r.ID)
		if err != nil || tr.RecordingAvailable == nil || !*tr.RecordingAvailable ||
			tr.TranscriptState != store.EvidenceUnavailable || tr.Stdout != "" || tr.Exit == nil || *tr.Exit != exit {
			t.Errorf("invalid transcript %q: %+v, %v", invalid, tr, err)
		}
	}
	r.RecordingState = store.EvidenceUnavailable
	if err := st.PutSession(r); err != nil {
		t.Fatal(err)
	}
	before := files.reads
	if f, err := g.RecordingFor(allowed, r.ID); f != nil || !errors.Is(err, ErrRecordingUnavailable) || files.reads != before {
		t.Fatalf("a failed capture reopened a potentially corrupt file: %v, %v", f, err)
	}
}

func TestSessionDetailBoundsContextAwareStorage(t *testing.T) {
	files := &cancelledReplay{Files: DirFiles{Dir: t.TempDir()}}
	g := &Gate{d: Deps{Files: files}}
	now := time.Now()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tr := g.sessionDetail(ctx, store.SessionRecord{ID: "abc", Ended: now, Recording: "abc",
		RecordingState: store.EvidenceComplete, TranscriptState: store.EvidenceComplete}, "")
	if !errors.Is(files.ctxErr, context.Canceled) || tr.RecordingAvailable == nil || *tr.RecordingAvailable ||
		tr.TranscriptState != store.EvidenceUnavailable {
		t.Fatalf("a cancelled detail attempted unbounded storage I/O: %+v, %v", tr, files.ctxErr)
	}
}

type cancelledReplay struct {
	Files
	ctxErr error
}

func (f *cancelledReplay) ReadFileContext(ctx context.Context, _ string) ([]byte, error) {
	f.ctxErr = ctx.Err()
	return nil, f.ctxErr
}
