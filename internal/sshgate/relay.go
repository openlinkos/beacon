package sshgate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/store"
)

// launcher starts an interactive shell for a session with a terminal. With bash, it marks the prompt,
// the start of each command with the command line (from the history, which is why HISTCONTROL is
// cleared) and its end with the exit status (OSC 133, as terminals' shell integration does), after
// reading the login files as a login shell would; without bash, it starts the user's shell. It is run
// by the user's login shell, so it is plain POSIX sh.
const launcher = `if command -v bash >/dev/null 2>&1 && f=$(mktemp 2>/dev/null); then
cat > "$f" <<'BEACON_RC'
rm -f -- "$BEACON_RC"; unset BEACON_RC
if [ -f /etc/profile ]; then . /etc/profile; fi
if [ -f ~/.bash_profile ]; then . ~/.bash_profile
elif [ -f ~/.bash_login ]; then . ~/.bash_login
elif [ -f ~/.profile ]; then . ~/.profile
fi
HISTCONTROL=
__beacon_prompt() { printf '\033]133;D;%s\007\033]133;A\007' "$?"; }
__beacon_command() { local c; c=$(HISTTIMEFORMAT= builtin history 1); c=${c#*[0-9]  }; c=${c//$'\a'/}; c=${c//$'\e'/}; printf '\033]133;C;%s\007' "$c"; }
PROMPT_COMMAND="__beacon_prompt${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
PS1="$PS1"'\[\033]133;B\007\]'
PS0='$(__beacon_command)'"${PS0:-}"
BEACON_RC
BEACON_RC=$f exec bash --noprofile --rcfile "$f" -i
fi
exec "${SHELL:-/bin/sh}" -l`

// session is one session on a machine, through the SSH channel or a tool: it records what passes and
// files the result.
type session struct {
	g          *Gate
	rec        store.SessionRecord
	cols, rows int
	pty        bool

	mu      sync.Mutex
	started bool
	cast    *recorder
	shell   *shellLog
	stdout  strings.Builder
	stderr  strings.Builder
	cut     bool
	in, out atomic.Int64
}

func (g *Gate) newSession(who Actor, m store.Machine, id string) *session {
	return &session{g: g, rec: store.SessionRecord{ID: id, Agent: who.Name, Via: who.Via, Machine: m.Name, Started: g.now().UTC()}}
}

// start opens the recording once the session says what it is: exec, shell, sftp, forward or file.
func (s *session) start(kind, command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	s.rec.Kind, s.rec.Command = kind, command
	day := s.rec.Started.Format("2006/01/02")
	dir := filepath.Join(s.g.d.Dir, filepath.FromSlash(day))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.g.d.Log.Error("cannot record a session", "session", s.rec.ID, "err", err)
	} else {
		cols, rows := s.cols, s.rows
		if cols <= 0 || rows <= 0 {
			cols, rows = 120, 40
		}
		title := fmt.Sprintf("%s on %s", s.rec.Agent, s.rec.Machine)
		r, err := newRecorder(filepath.Join(dir, s.rec.ID+".cast"), cols, rows, command, title, s.g.now)
		if err != nil {
			s.g.d.Log.Error("cannot record a session", "session", s.rec.ID, "err", err)
		} else {
			s.cast = r
			s.rec.Recording = day + "/" + s.rec.ID
		}
	}
	if kind == "shell" && s.pty {
		s.shell = &shellLog{rec: s.cast, now: s.g.now}
	}
	_ = s.g.d.Store.PutSession(s.rec)
}

// parts returns the recording, the shell's log and the kind, which start sets on another goroutine.
func (s *session) parts() (*recorder, *shellLog, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cast, s.shell, s.rec.Kind
}

// input takes what the agent sends: typed into a terminal, or a command's stdin.
func (s *session) input(p []byte) {
	s.in.Add(int64(len(p)))
	cast, shell, kind := s.parts()
	if kind == "sftp" || kind == "" {
		return
	}
	cast.event("i", string(p))
	if shell != nil {
		shell.input(p)
	}
}

// output takes what the machine sends and returns what the agent should see (an interactive shell's
// marks removed).
func (s *session) output(p []byte, stderr bool) []byte {
	s.out.Add(int64(len(p)))
	cast, shell, kind := s.parts()
	if kind == "sftp" || kind == "" {
		return p
	}
	if shell != nil && !stderr {
		p = shell.output(p)
	} else {
		s.mu.Lock()
		b := &s.stdout
		if stderr {
			b = &s.stderr
		}
		room := maxText - b.Len()
		if len(p) > room {
			s.cut = true
		}
		if room > 0 {
			b.Write(p[:min(len(p), room)])
		}
		s.mu.Unlock()
	}
	if len(p) > 0 {
		cast.event("o", string(p))
	}
	return p
}

// finish files the session: the transcript next to the recording, the record, the audit log, the chat.
func (s *session) finish(exit *int) Transcript {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return Transcript{}
	}
	rec, cast, shell, cut := s.rec, s.cast, s.shell, s.cut
	raw, stderr := s.stdout.String(), s.stderr.String()
	s.mu.Unlock()
	if exit != nil {
		cast.event("x", fmt.Sprint(*exit))
	}
	cast.close()
	rec.Ended = s.g.now().UTC()
	rec.Exit = exit
	rec.BytesIn, rec.BytesOut = s.in.Load(), s.out.Load()
	t := Transcript{Truncated: cut || cast.truncated()}
	if shell != nil {
		t.Commands = shell.done()
		rec.Commands = len(t.Commands)
	} else if rec.Kind == "exec" || rec.Kind == "file" {
		t.raw = raw
		t.Stdout, t.Stderr = clean(raw), clean(stderr)
	}
	t.SessionRecord = rec
	s.mu.Lock()
	s.rec = rec
	s.mu.Unlock()
	s.g.file(t)
	_ = s.g.d.Store.PutSession(rec)
	_ = s.g.d.Store.Audit(store.AuditEntry{Actor: rec.Agent, Via: rec.Via, Action: "ssh_" + rec.Kind, Target: rec.Machine,
		Detail: short(rec.Command, 200)})
	if s.g.d.Metrics != nil {
		s.g.d.Metrics.Inc("beacon_ssh_sessions_total", "Sessions on machines through the SSH channel, by kind and how they came",
			metrics.Labels("kind", rec.Kind, "via", rec.Via))
	}
	s.g.echo.add(rec)
	return t
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

type teeReader struct {
	r io.Reader
	f func([]byte)
}

func (t teeReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.f(p[:n])
	}
	return n, err
}

// relay copies src to dst through f, which may change what passes.
func relay(dst io.Writer, src io.Reader, f func([]byte) []byte) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if out := f(buf[:n]); len(out) > 0 {
				if _, werr := dst.Write(out); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// session relays one session channel between the agent and the machine.
func (c *conn) session(nc ssh.NewChannel) {
	tc, err := c.target()
	if err != nil {
		refuse(nc, err)
		return
	}
	tch, treqs, err := tc.OpenChannel("session", nil)
	if err != nil {
		refuse(nc, fmt.Errorf("%w: %v", ErrUnreachable, err)) //nolint:errorlint // one wrapped error is enough
		return
	}
	ach, areqs, err := nc.Accept()
	if err != nil {
		_ = tch.Close()
		return
	}
	s := c.g.newSession(c.who, c.machine, c.next())
	go func() {
		for r := range areqs {
			typ, payload := r.Type, r.Payload
			switch typ {
			case "env": // the machine's own locale and environment, not the agent's
				if r.WantReply {
					_ = r.Reply(false, nil)
				}
				continue
			case "pty-req":
				var p struct {
					Term                 string
					Cols, Rows, Wpx, Hpx uint32
					Modes                string
				}
				if ssh.Unmarshal(payload, &p) == nil {
					s.cols, s.rows, s.pty = int(p.Cols), int(p.Rows), true
				}
			case "window-change":
				var p struct{ Cols, Rows, Wpx, Hpx uint32 }
				if cast, _, _ := s.parts(); ssh.Unmarshal(payload, &p) == nil {
					cast.event("r", fmt.Sprintf("%dx%d", p.Cols, p.Rows))
				}
			case "shell":
				s.start("shell", "")
				if s.pty {
					typ, payload = "exec", ssh.Marshal(struct{ Command string }{launcher})
				}
			case "exec":
				var p struct{ Command string }
				_ = ssh.Unmarshal(payload, &p)
				s.start("exec", p.Command)
			case "subsystem":
				var p struct{ Name string }
				_ = ssh.Unmarshal(payload, &p)
				s.start(p.Name, p.Name)
			}
			ok, err := tch.SendRequest(typ, r.WantReply, payload)
			if r.WantReply {
				_ = r.Reply(ok && err == nil, nil)
			}
		}
	}()
	var exit atomic.Pointer[int]
	copied := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for r := range treqs {
			if r.Type == "exit-status" {
				var p struct{ Status uint32 }
				if ssh.Unmarshal(r.Payload, &p) == nil {
					n := int(p.Status)
					exit.Store(&n)
				}
			}
			ok, _ := ach.SendRequest(r.Type, r.WantReply, r.Payload)
			if r.WantReply {
				_ = r.Reply(ok, nil)
			}
		}
		<-copied
		_ = ach.Close()
	}()
	go func() {
		_, _ = io.Copy(tch, teeReader{ach, s.input})
		_ = tch.CloseWrite()
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		relay(ach.Stderr(), tch.Stderr(), func(p []byte) []byte { return s.output(p, true) })
	}()
	relay(ach, tch, func(p []byte) []byte { return s.output(p, false) })
	wg.Wait()
	_ = ach.CloseWrite()
	close(copied)
	<-done
	_ = tch.Close()
	s.finish(exit.Load())
}

// forward relays a forwarded port (ssh -L) to the machine and files where it went.
func (c *conn) forward(nc ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	_ = ssh.Unmarshal(nc.ExtraData(), &p)
	tc, err := c.target()
	if err != nil {
		refuse(nc, err)
		return
	}
	tch, treqs, err := tc.OpenChannel("direct-tcpip", nc.ExtraData())
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ach, areqs, err := nc.Accept()
	if err != nil {
		_ = tch.Close()
		return
	}
	go ssh.DiscardRequests(areqs)
	go ssh.DiscardRequests(treqs)
	s := c.g.newSession(c.who, c.machine, c.next())
	s.start("forward", fmt.Sprintf("%s:%d", p.Host, p.Port))
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(tch, ach)
		s.in.Add(n)
		_ = tch.CloseWrite()
		close(done)
	}()
	n, _ := io.Copy(ach, tch)
	s.out.Add(n)
	_ = ach.CloseWrite()
	select {
	case <-done:
	case <-time.After(time.Minute):
	}
	_ = ach.Close()
	_ = tch.Close()
	s.finish(nil)
}
