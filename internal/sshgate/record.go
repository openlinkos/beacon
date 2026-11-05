package sshgate

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxRecording caps one recording; output beyond it still flows, unrecorded, and the transcript says so.
const maxRecording = 16 << 20

// recorder writes a session as asciicast v3: a header line, then [interval, code, data] events, the
// interval counted from the previous event. Codes: o output, i input, r resize, m marker (a command
// starts), x exit status.
type recorder struct {
	mu      sync.Mutex
	f       io.WriteCloser
	last    time.Time
	written int64
	full    bool
	now     func() time.Time
}

// newRecorder writes the asciicast to w, whose making and closing (a file under the recordings
// directory, an upload that Close sends) is the store's business.
func newRecorder(w io.WriteCloser, cols, rows int, command, title string, now func() time.Time) (*recorder, error) {
	hdr, _ := json.Marshal(map[string]any{"version": 3, "term": map[string]any{"cols": cols, "rows": rows, "type": "xterm-256color"},
		"timestamp": now().Unix(), "command": command, "title": title})
	r := &recorder{f: w, last: now(), now: now}
	r.write(append(hdr, '\n'))
	return r, nil
}

// close finishes the recording: for a file it closes it, for an upload it sends it.
func (r *recorder) close() error {
	if r == nil || r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func (r *recorder) write(b []byte) {
	if r.full {
		return
	}
	if r.written+int64(len(b)) > maxRecording {
		r.full = true
		return
	}
	n, _ := r.f.Write(b)
	r.written += int64(n)
}

func (r *recorder) event(code, data string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	b, _ := json.Marshal([]any{math.Round(now.Sub(r.last).Seconds()*1e6) / 1e6, code, data})
	r.last = now
	r.write(append(b, '\n'))
}

// truncated reports whether the recording reached its cap.
func (r *recorder) truncated() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.full
}

// Command is one command run at an interactive shell's prompt, as the shell-integration marks delimit
// it. Inputs are the lines typed into the program while it ran (into psql, say).
type Command struct {
	Command string    `json:"command"`
	Inputs  []string  `json:"inputs,omitempty"`
	Output  string    `json:"output"`
	Exit    *int      `json:"exit,omitempty"`
	Started time.Time `json:"started"`
	Ms      int64     `json:"ms"`
}

// maxText caps the text a transcript keeps of one stream or one command's output.
const maxText = 1 << 20

// shellLog follows the marks an interactive bash prints when Beacon starts it (OSC 133, as terminals'
// shell integration: A a prompt starts, B typing starts, C;command the command runs, D;status it ended),
// strips them from what the agent sees, and lists the commands. The command line comes from the shell;
// what was typed since the prompt stands in when the shell does not say (its history is off, say).
type shellLog struct {
	mu       sync.Mutex
	state    byte // 0 idle, 'B' typing a command, 'C' a command runs
	typed    strings.Builder
	line     strings.Builder
	cur      *Command
	carry    []byte
	rec      *recorder
	now      func() time.Time
	commands []Command
}

var markStart = []byte("\x1b]133;")

// maxMark is the longest mark kept whole across reads: a C mark carries the command line.
const maxMark = 64 << 10

// output takes bytes from the shell and returns them without the marks.
func (l *shellLog) output(p []byte) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	data := append(append([]byte(nil), l.carry...), p...)
	l.carry = nil
	var out []byte
	for {
		i := bytes.Index(data, markStart)
		if i < 0 {
			k := 0
			for n := min(len(data), len(markStart)-1); n > 0; n-- {
				if bytes.HasPrefix(markStart, data[len(data)-n:]) {
					k = n
					break
				}
			}
			out = append(out, l.plain(data[:len(data)-k])...)
			l.carry = append([]byte(nil), data[len(data)-k:]...)
			return out
		}
		out = append(out, l.plain(data[:i])...)
		rest := data[i+len(markStart):]
		end := bytes.IndexAny(rest, "\x07\x1b")
		if end < 0 || (rest[end] == 0x1b && end+1 >= len(rest)) {
			if len(rest) > maxMark { // not a mark of ours after all
				out = append(out, l.plain(data[i:])...)
				return out
			}
			l.carry = append([]byte(nil), data[i:]...)
			return out
		}
		skip := end + 1
		if rest[end] == 0x1b {
			skip = end + 2
		}
		l.mark(string(rest[:end]))
		data = rest[skip:]
	}
}

func (l *shellLog) plain(b []byte) []byte {
	if l.state == 'C' && l.cur != nil && len(l.cur.Output) < maxText {
		l.cur.Output += string(b)
	}
	return b
}

func (l *shellLog) mark(body string) {
	switch {
	case body == "A": // a new prompt; a prompt redrawn (after a resize, say) repeats only B
		l.state = 'B'
		l.typed.Reset()
	case body == "B":
		l.state = 'B'
	case body == "C" || strings.HasPrefix(body, "C;"):
		cmd := strings.TrimSpace(strings.TrimPrefix(body, "C;"))
		if body == "C" || cmd == "" {
			cmd = strings.TrimSpace(strings.ReplaceAll(l.typed.String(), "\r", "\n"))
		}
		l.cur = &Command{Command: cmd, Started: l.now()}
		l.state = 'C'
		l.line.Reset()
		l.rec.event("m", cmd)
	case strings.HasPrefix(body, "D"):
		if l.state == 'C' && l.cur != nil {
			if n, err := strconv.Atoi(strings.TrimPrefix(body, "D;")); err == nil {
				l.cur.Exit = &n
			}
			l.cur.Ms = l.now().Sub(l.cur.Started).Milliseconds()
			l.cur.Output = clean(l.cur.Output)
			l.commands = append(l.commands, *l.cur)
			l.cur = nil
		}
		l.state = 0
	}
}

// input takes the bytes the agent typed.
func (l *shellLog) input(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.state {
	case 'B':
		if l.typed.Len() < maxText {
			l.typed.Write(p)
		}
	case 'C':
		if l.cur == nil {
			return
		}
		for _, c := range string(p) {
			if c == '\r' || c == '\n' {
				if s := strings.TrimSpace(l.line.String()); s != "" {
					l.cur.Inputs = append(l.cur.Inputs, s)
				}
				l.line.Reset()
				continue
			}
			l.line.WriteRune(c)
		}
	}
}

// done returns the commands, with the one still running (when the shell ended inside it).
func (l *shellLog) done() []Command {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]Command(nil), l.commands...)
	if l.cur != nil {
		c := *l.cur
		c.Output = clean(c.Output)
		c.Ms = l.now().Sub(c.Started).Milliseconds()
		out = append(out, c)
	}
	return out
}

var escapes = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[@-Z\\^_=>]`)

// clean turns terminal output into plain text for agents: no escape sequences, no carriage returns
// (a line redrawn in place keeps its last state), no backspaces.
func clean(s string) string {
	s = escapes.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if j := strings.LastIndexByte(strings.TrimRight(l, "\r"), '\r'); j >= 0 {
			l = l[j+1:]
		}
		l = strings.TrimRight(l, "\r")
		if strings.IndexByte(l, '\b') >= 0 {
			var b []rune
			for _, c := range l {
				if c == '\b' {
					if len(b) > 0 {
						b = b[:len(b)-1]
					}
					continue
				}
				b = append(b, c)
			}
			l = string(b)
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}
