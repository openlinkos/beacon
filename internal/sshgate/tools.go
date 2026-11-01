package sshgate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

// The tools behave like an agent's own: run a command, read a file with line numbers, write a file,
// replace an exact string in one. Each call is a session of its own, recorded like any other.

const (
	defaultTimeout = 2 * time.Minute
	maxTimeout     = 10 * time.Minute
	maxToolText    = 30000 // characters of each stream a tool returns; the transcript keeps more
	maxFile        = 1 << 20
	defaultLines   = 2000
)

// quote makes s one word for a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// script runs a shell script with arguments, whatever the login shell of the machine's user is.
func script(body string, args ...string) string {
	q := make([]string, 0, len(args))
	for _, a := range args {
		q = append(q, quote(a))
	}
	return "sh -c " + quote(body) + " beacon " + strings.Join(q, " ")
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

// execute runs command on the machine as one recorded session and returns its transcript. Label is what
// the record calls it (the command itself, or "read /etc/hosts" for a file tool). A command still
// running at the timeout is sent SIGKILL and abandoned; its transcript has no exit status.
func (g *Gate) execute(ctx context.Context, who Actor, m store.Machine, kind, label, command string, stdin []byte,
	timeout time.Duration) (Transcript, bool, error) {
	conn := newID(g.now())
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	untrack := g.track(cancel)
	defer untrack()
	cl, err := g.dial(ctx, m, who, conn)
	if err != nil {
		return Transcript{}, false, err
	}
	defer func() { _ = cl.Close() }()
	sess, err := cl.NewSession()
	if err != nil {
		return Transcript{}, false, fmt.Errorf("%w: %v", ErrUnreachable, err) //nolint:errorlint // one wrapped error is enough
	}
	s := g.newSession(who, m, conn+"-1")
	s.start(kind, label)
	sess.Stdout = writerFunc(func(p []byte) { s.output(p, false) })
	sess.Stderr = writerFunc(func(p []byte) { s.output(p, true) })
	if stdin != nil {
		sess.Stdin = teeReader{bytes.NewReader(stdin), s.input}
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(command) }()
	timedOut := false
	select {
	case err = <-done:
	case <-ctx.Done():
		timedOut = true
		_ = sess.Signal(ssh.SIGKILL)
		_ = cl.Close()
		err = <-done
	}
	var exit *int
	var ee *ssh.ExitError
	switch {
	case timedOut:
	case err == nil:
		n := 0
		exit = &n
	case errors.As(err, &ee):
		n := ee.ExitStatus()
		exit = &n
	}
	t := s.finish(exit)
	if !timedOut && exit == nil {
		return t, false, fmt.Errorf("%w: %v", ErrUnreachable, err) //nolint:errorlint // one wrapped error is enough
	}
	return t, timedOut, nil
}

// cut shortens a tool's output, keeping its start and its end.
func cut(s, session string) (string, bool) {
	r := []rune(s)
	if len(r) <= maxToolText {
		return s, false
	}
	head, tail := maxToolText/3, maxToolText-maxToolText/3
	return string(r[:head]) + fmt.Sprintf("\n[... %d characters cut; get_session %s has the whole output ...]\n", len(r)-head-tail, session) +
		string(r[len(r)-tail:]), true
}

// RunInput runs a command. Timeout is in seconds (default 120, at most 600).
type RunInput struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`
	Stdin   string `json:"stdin,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// RunResult is what a command did. ExitCode is -1 when it timed out.
type RunResult struct {
	Machine   string `json:"machine"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Ms        int64  `json:"duration_ms"`
	Session   string `json:"session"`
}

// Run runs a command on a machine, as the machine's user, in a shell without a terminal.
func (g *Gate) Run(ctx context.Context, who Actor, machine string, in RunInput) (RunResult, error) {
	m, err := g.machineFor(who, machine)
	if err != nil {
		return RunResult{}, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return RunResult{}, fmt.Errorf("%w: command is empty", ErrBadInput)
	}
	timeout := defaultTimeout
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout)*time.Second, maxTimeout)
	}
	cmd := in.Command
	if in.Cwd != "" {
		cmd = "cd " + quote(in.Cwd) + " && " + cmd
	}
	var stdin []byte
	if in.Stdin != "" {
		stdin = []byte(in.Stdin)
	}
	t, timedOut, err := g.execute(ctx, who, m, "exec", in.Command, cmd, stdin, timeout)
	if err != nil {
		return RunResult{}, err
	}
	out := RunResult{Machine: m.Name, ExitCode: -1, TimedOut: timedOut, Truncated: t.Truncated, Ms: t.Ended.Sub(t.Started).Milliseconds(), Session: t.ID}
	if t.Exit != nil {
		out.ExitCode = *t.Exit
	}
	var c1, c2 bool
	out.Stdout, c1 = cut(t.Stdout, t.ID)
	out.Stderr, c2 = cut(t.Stderr, t.ID)
	out.Truncated = out.Truncated || c1 || c2
	return out, nil
}

// fileError turns the exit status of a file script into an error.
func fileError(t Transcript, path string) error {
	if t.Exit == nil {
		return fmt.Errorf("%w: reading %s took too long", ErrUnreachable, path)
	}
	switch *t.Exit {
	case 0:
		return nil
	case 66:
		return fmt.Errorf("%w: %s", ErrNoFile, path)
	case 65:
		return fmt.Errorf("%w: %s", ErrNotRegular, path)
	case 67:
		return fmt.Errorf("%w: %s; run a command (od, xxd, file) to look at it", ErrBinary, path)
	case 68:
		return fmt.Errorf("%w: %s is over 1 MiB; read it in parts or edit it with a command", ErrTooLarge, path)
	case 75:
		return fmt.Errorf("%w: %s; read it again", ErrChanged, path)
	}
	return fmt.Errorf("%w: %s: %s", ErrBadInput, path, strings.TrimSpace(t.Stderr))
}

const checkFile = `f=$1
if [ ! -e "$f" ]; then echo "no such file: $f" >&2; exit 66; fi
if [ ! -f "$f" ]; then echo "not a regular file: $f" >&2; exit 65; fi
if [ "$(head -c 8000 < "$f" | tr -d -c '\000' | wc -c)" -gt 0 ]; then echo "binary file: $f" >&2; exit 67; fi
`

// FileView is part of a file, as an agent's own read tool shows it: each line numbered, a tab, the line.
type FileView struct {
	Machine    string `json:"machine"`
	Path       string `json:"path"`
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	Lines      int    `json:"lines"`
	TotalLines int    `json:"total_lines"`
	More       bool   `json:"more,omitempty"` // lines follow the last one shown
	Session    string `json:"session"`
}

// ReadFile reads lines of a text file, from line offset (1 is the first), at most limit (default 2000).
// Lines longer than 2000 characters are cut.
func (g *Gate) ReadFile(ctx context.Context, who Actor, machine, path string, offset, limit int) (FileView, error) {
	m, err := g.machineFor(who, machine)
	if err != nil {
		return FileView{}, err
	}
	if path == "" {
		return FileView{}, fmt.Errorf("%w: path is empty", ErrBadInput)
	}
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = defaultLines
	}
	limit = min(limit, 10000)
	body := checkFile + `awk -v a="$2" -v n="$3" 'NR>=a && NR<a+n { l=$0; if (length(l)>2000) l=substr(l,1,2000) "..."; printf "%6d\t%s\n", NR, l } END { printf "%d\n", NR > "/dev/stderr" }' < "$f"`
	t, _, err := g.execute(ctx, who, m, "file", "read "+path, script(body, path, strconv.Itoa(offset), strconv.Itoa(limit)), nil, time.Minute)
	if err != nil {
		return FileView{}, err
	}
	if err := fileError(t, path); err != nil {
		return FileView{}, err
	}
	total, _ := strconv.Atoi(strings.TrimSpace(lastLine(t.Stderr)))
	content, _ := cut(t.Stdout, t.ID)
	shown := strings.Count(t.Stdout, "\n")
	return FileView{Machine: m.Name, Path: path, Content: content, Offset: offset, Lines: shown, TotalLines: total,
		More: offset+shown-1 < total, Session: t.ID}, nil
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Written is what a write or an edit did.
type Written struct {
	Machine      string `json:"machine"`
	Path         string `json:"path"`
	Bytes        int    `json:"bytes"`
	Replacements int    `json:"replacements,omitempty"`
	Session      string `json:"session"`
}

// WriteFile writes content to a file, making its directory if needed. An existing file keeps its owner
// and mode.
func (g *Gate) WriteFile(ctx context.Context, who Actor, machine, path, content string) (Written, error) {
	m, err := g.machineFor(who, machine)
	if err != nil {
		return Written{}, err
	}
	if path == "" {
		return Written{}, fmt.Errorf("%w: path is empty", ErrBadInput)
	}
	if len(content) > maxFile {
		return Written{}, fmt.Errorf("%w: content is over 1 MiB; copy large files with scp or rsync", ErrTooLarge)
	}
	body := `f=$1
d=$(dirname -- "$f")
if [ ! -d "$d" ]; then mkdir -p -- "$d" || exit 73; fi
cat > "$f" || exit 74`
	t, _, err := g.execute(ctx, who, m, "file", "write "+path, script(body, path), []byte(content), time.Minute)
	if err != nil {
		return Written{}, err
	}
	if err := fileError(t, path); err != nil {
		return Written{}, err
	}
	return Written{Machine: m.Name, Path: path, Bytes: len(content), Session: t.ID}, nil
}

// EditInput replaces OldString with NewString in a file: exactly one occurrence, or every one with
// ReplaceAll.
type EditInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// EditFile edits a text file by exact replacement, as an agent's own edit tool does. It fails rather
// than overwrite a file that changed after it was read.
func (g *Gate) EditFile(ctx context.Context, who Actor, machine string, in EditInput) (Written, error) {
	m, err := g.machineFor(who, machine)
	if err != nil {
		return Written{}, err
	}
	switch {
	case in.Path == "":
		return Written{}, fmt.Errorf("%w: path is empty", ErrBadInput)
	case in.OldString == "":
		return Written{}, fmt.Errorf("%w: old_string is empty; write the whole file with write_file", ErrBadInput)
	case in.OldString == in.NewString:
		return Written{}, fmt.Errorf("%w: old_string and new_string are the same", ErrBadInput)
	}
	read := checkFile + `if [ "$(wc -c < "$f")" -gt 1048576 ]; then exit 68; fi
cksum < "$f" >&2
cat < "$f"`
	t, _, err := g.execute(ctx, who, m, "file", "read "+in.Path, script(read, in.Path), nil, time.Minute)
	if err != nil {
		return Written{}, err
	}
	if err := fileError(t, in.Path); err != nil {
		return Written{}, err
	}
	sum := strings.TrimSpace(lastLine(t.Stderr))
	raw, ok := g.rawStdout(t)
	if !ok {
		return Written{}, fmt.Errorf("%w: %s could not be read whole", ErrTooLarge, in.Path)
	}
	n := strings.Count(raw, in.OldString)
	switch {
	case n == 0:
		return Written{}, fmt.Errorf("%w: %s", ErrNoMatch, in.Path)
	case n > 1 && !in.ReplaceAll:
		return Written{}, fmt.Errorf("%w: %d times in %s; give more context or set replace_all", ErrAmbiguous, n, in.Path)
	}
	next := strings.Replace(raw, in.OldString, in.NewString, map[bool]int{true: -1, false: 1}[in.ReplaceAll])
	write := `f=$1
if [ "$(cksum < "$f")" != "$2" ]; then echo "the file changed since it was read" >&2; exit 75; fi
cat > "$f" || exit 74`
	w, _, err := g.execute(ctx, who, m, "file", "edit "+in.Path, script(write, in.Path, sum), []byte(next), time.Minute)
	if err != nil {
		return Written{}, err
	}
	if err := fileError(w, in.Path); err != nil {
		return Written{}, err
	}
	if !in.ReplaceAll {
		n = 1
	}
	return Written{Machine: m.Name, Path: in.Path, Bytes: len(next), Replacements: n, Session: w.ID}, nil
}

// rawStdout is a file tool's output exactly as the machine sent it (the transcript's is cleaned for
// reading, which an edit must not do).
func (g *Gate) rawStdout(t Transcript) (string, bool) {
	if t.Truncated {
		return "", false
	}
	return t.raw, true
}
