package sshgate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

var toolActor = Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}

// TestRecordingLimitClosesTheSession: input that would take the recording past its cap is neither
// recorded nor passed on; the session is closed and says why.
func TestRecordingLimitClosesTheSession(t *testing.T) {
	needShell(t)
	e := setup(t)
	marker := filepath.Join(e.tg.home, "after-the-limit")
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
	in, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("sh"); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer func() { _ = in.Close() }()
		pad := bytes.Repeat([]byte("# nothing to see here, only padding\n"), 1<<10)
		for sent := 0; sent < maxRecording+1<<20; sent += len(pad) {
			if _, err := in.Write(pad); err != nil {
				return
			}
		}
		_, _ = fmt.Fprintf(in, "touch %s\n", quote(marker))
	}()
	err = s.Wait()
	var ee *ssh.ExitError
	if errors.As(err, &ee) && ee.ExitStatus() == 0 || err == nil {
		t.Fatalf("the session went on past the limit: %v", err)
	}
	if !strings.Contains(stderr.String(), ErrRecordingLimit.Error()) {
		t.Errorf("the agent was not told why: %q", stderr.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("input past the limit reached the machine: %v", err)
	}
	recs := waitSessions(t, e.st, 1)
	if len(recs) != 1 || recs[0].Stopped != store.StoppedRecordingLimit || recs[0].RecordingState != store.EvidenceTruncated {
		t.Fatalf("record %+v", recs)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, filepath.FromSlash(recs[0].Recording)+".cast"))
	if err != nil {
		t.Fatal(err)
	}
	if n := castData(t, b); n > maxRecording {
		t.Errorf("the recording holds %d bytes of what passed", n)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, `"m","beacon: the recording reached its limit`) {
		t.Errorf("the recording does not end with the limit: %q", last)
	}
	if bytes.Contains(b, []byte("after-the-limit")) {
		t.Error("the recording holds input that never passed")
	}
}

// TestRecordingLimitStopsATool: a tool call whose output would take its recording past the cap is stopped
// and says so.
func TestRecordingLimitStopsATool(t *testing.T) {
	needShell(t)
	e := setup(t)
	_, err := e.g.Run(context.Background(), toolActor, "box", RunInput{Command: "head -c 20000000 /dev/zero | tr '\\000' a; echo finished >&2"})
	if !errors.Is(err, ErrRecordingLimit) {
		t.Fatalf("run: %v", err)
	}
	recs := waitSessions(t, e.st, 1)
	if len(recs) != 1 || recs[0].Stopped != store.StoppedRecordingLimit || recs[0].RecordingState != store.EvidenceTruncated || recs[0].Exit != nil {
		t.Fatalf("record %+v", recs)
	}
	if !strings.Contains(err.Error(), recs[0].ID) {
		t.Errorf("the error does not name the session: %v", err)
	}
}

// TestExecTranscriptKeepsStdin: a command's transcript keeps what it was sent, over ssh and through the tool.
func TestExecTranscriptKeepsStdin(t *testing.T) {
	needShell(t)
	e := setup(t)
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	s.Stdin = strings.NewReader("echo from a script\n")
	if err := s.Run("sh >/dev/null"); err != nil {
		t.Fatal(err)
	}
	_ = cl.Close()
	recs := waitSessions(t, e.st, 1)
	tr, err := e.g.Session(recs[0].ID)
	if err != nil || tr.Stdin != "echo from a script\n" {
		t.Fatalf("ssh transcript: %+v %v", tr, err)
	}
	r, err := e.g.Run(context.Background(), toolActor, "box", RunInput{Command: "cat >/dev/null", Stdin: "tool input\n"})
	if err != nil {
		t.Fatal(err)
	}
	if tr, err := e.g.Session(r.Session); err != nil || tr.Stdin != "tool input\n" {
		t.Fatalf("tool transcript: %+v %v", tr, err)
	}
}

// TestRecordingKeepsShellMarks: the agent sees a shell's output without the shell-integration marks; the
// recording keeps them as the machine sent them, and the transcript's commands are labelled a guide.
func TestRecordingKeepsShellMarks(t *testing.T) {
	e := setup(t)
	m, _ := e.st.Machine("box")
	s := e.g.newSession(Actor{Name: "claude-code", Via: "ssh"}, m, newID(time.Now())+"-1")
	s.pty = true
	if err := s.start("shell", ""); err != nil {
		t.Fatal(err)
	}
	var seen []byte
	for _, chunk := range []string{"\x1b]133;A\a$ \x1b]133;B\a", "ls\r\n\x1b]133;C;ls\afile\r\n\x1b]133;D;0\a"} {
		out, ok := s.output([]byte(chunk), false)
		if !ok {
			t.Fatal("output refused")
		}
		seen = append(seen, out...)
	}
	if strings.Contains(string(seen), "\x1b]133") || !strings.Contains(string(seen), "file") {
		t.Fatalf("the agent saw %q", seen)
	}
	tr := s.finish(nil, true)
	if !tr.CommandsAdvisory || len(tr.Commands) != 1 || tr.Commands[0].Command != "ls" {
		t.Fatalf("transcript %+v", tr)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, filepath.FromSlash(tr.Recording)+".cast"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`\u001b]133;C;ls\u0007file`)) {
		t.Fatalf("the recording lost the raw output:\n%s", b)
	}
	if saved, err := e.g.Session(tr.ID); err != nil || !saved.CommandsAdvisory {
		t.Fatalf("read back: %+v %v", saved, err)
	}
}

// castData is how many bytes of input and output a recording holds, as they passed (for text; binary
// data the JSON could not keep byte for byte counts as it reads back).
func castData(t *testing.T, b []byte) int {
	t.Helper()
	n := 0
	for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if i == 0 {
			continue
		}
		var ev []any
		if err := json.Unmarshal([]byte(line), &ev); err != nil || len(ev) != 3 {
			t.Fatalf("event %d: %q: %v", i, line, err)
		}
		if code, _ := ev[1].(string); code == "i" || code == "o" {
			data, _ := ev[2].(string)
			n += len(data)
		}
	}
	return n
}

// TestRecordingCountsDataAsItPassed: the cap counts an event's data as it passed, not as JSON escapes it,
// so binary data takes as much of it as it did on the wire; the file stays within six times the cap.
func TestRecordingCountsDataAsItPassed(t *testing.T) {
	var file bytes.Buffer
	r, err := newRecorder(nopCloser{&file}, 80, 24, "cat", "test", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	chunk := string(bytes.Repeat([]byte{1}, 32<<10)) // six bytes each in the file: \u0001
	sent := 0
	for sent+len(chunk) <= 8<<20 {
		if !r.event("i", chunk) {
			t.Fatalf("the recording was full after %d bytes of input", sent)
		}
		sent += len(chunk)
	}
	if r.used > int64(sent)+int64(sent/len(chunk))*64 {
		t.Fatalf("%d bytes passed cost %d of the cap", sent, r.used)
	}
	if file.Len() < 5*sent {
		t.Fatalf("the file is %d bytes for %d bytes of control characters", file.Len(), sent)
	}
	for r.event("o", chunk) {
	}
	if !r.truncated() || r.used > maxRecording || int64(file.Len()) > 6*r.used {
		t.Fatalf("full %v at %d of the cap, file %d bytes", r.truncated(), r.used, file.Len())
	}
	line, cost := eventLine(0.5, "o", "<a&b>\n")
	if string(line) != "[0.5,\"o\",\"<a&b>\\n\"]\n" || cost != int64(len(line))-9+6 {
		t.Fatalf("event %q costs %d", line, cost)
	}
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// TestRecordingTakesABinaryUpload: a binary upload of half the cap passes whole, although JSON escapes it
// to more than the cap in the file.
func TestRecordingTakesABinaryUpload(t *testing.T) {
	needShell(t)
	e := setup(t)
	data := make([]byte, maxRecording/2)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr syncBuffer
	s.Stdout, s.Stderr, s.Stdin = &stdout, &stderr, bytes.NewReader(data)
	if err := s.Run("wc -c"); err != nil {
		t.Fatalf("the upload was stopped: %v (%s)", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != strconv.Itoa(len(data)) {
		t.Fatalf("the machine took %s bytes of %d", got, len(data))
	}
	recs := waitSessions(t, e.st, 1)
	if r := recs[0]; r.Stopped != "" || r.RecordingState != store.EvidenceComplete || r.BytesIn != int64(len(data)) {
		t.Fatalf("record %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, filepath.FromSlash(recs[0].Recording)+".cast"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) <= maxRecording {
		t.Fatalf("the upload took only %d bytes of the file: not the binary it was meant to be", len(b))
	}
}
