package sshgate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

func signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// target is a machine for the tests: an SSH server that trusts only the CA and runs what it is asked with
// sh, as sshd does. With a terminal requested it merges stderr into stdout, as a terminal would.
type target struct {
	addr    string
	hostKey ssh.Signer
	cfg     *ssh.ServerConfig
	home    string
	mu      sync.Mutex
	keyIDs  []string
	sources []string // the source-address option of each certificate accepted
}

func newTarget(t *testing.T, ca ssh.PublicKey) *target {
	t.Helper()
	tg := &target{hostKey: signer(t), home: t.TempDir()}
	checker := &ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), ca.Marshal()) }}
	tg.cfg = &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		p, err := checker.Authenticate(c, k)
		if err == nil {
			tg.mu.Lock()
			tg.keyIDs = append(tg.keyIDs, k.(*ssh.Certificate).KeyId)
			tg.sources = append(tg.sources, k.(*ssh.Certificate).CriticalOptions["source-address"])
			tg.mu.Unlock()
		}
		return p, err
	}}
	tg.cfg.AddHostKey(tg.hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	tg.addr = ln.Addr().String()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go tg.serve(nc)
		}
	}()
	return tg
}

// serve answers one connection to the machine's sshd: dialled, or handed over by a connector.
func (tg *target) serve(nc net.Conn) {
	cfg, home := tg.cfg, tg.home
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.Prohibited, "sessions only")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			pty := false
			for r := range creqs {
				switch r.Type {
				case "pty-req":
					pty = true
					_ = r.Reply(true, nil)
				case "exec", "shell":
					var p struct{ Command string }
					_ = ssh.Unmarshal(r.Payload, &p)
					args := []string{"-c", p.Command}
					if r.Type == "shell" {
						args = []string{"-l"}
					}
					cmd := exec.Command("sh", args...)
					cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=dumb", "SHELL=/bin/sh"}
					cmd.Dir = home
					cmd.Stdout = ch
					cmd.Stderr = ch.Stderr()
					if pty {
						cmd.Stderr = ch
					}
					stdin, err := cmd.StdinPipe() // like sshd, do not wait for the client's stdin once the command exits
					if err != nil {
						_ = r.Reply(false, nil)
						continue
					}
					go func() { _, _ = io.Copy(stdin, ch); _ = stdin.Close() }()
					_ = r.Reply(true, nil)
					code := 0
					if err := cmd.Run(); err != nil {
						var ee *exec.ExitError
						code = 255
						if errors.As(err, &ee) {
							code = ee.ExitCode()
						}
					}
					_ = ch.CloseWrite()
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
					_ = ch.Close()
					return
				default:
					if r.WantReply {
						_ = r.Reply(false, nil)
					}
				}
			}
		}()
	}
}

type posted struct {
	mu   sync.Mutex
	msgs []string
}

func (p *posted) Send(_ context.Context, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, text)
	return nil
}

func (p *posted) all() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.msgs, "\n---\n")
}

type env struct {
	g        *Gate
	st       *store.Store
	tg       *target
	addr     string // the channel's SSH listener
	agentKey ssh.Signer
	bot      *posted
	dir      string
}

func needShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test machine runs commands with sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
}

func setup(t *testing.T, scopes ...string) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ca := signer(t)
	e := &env{st: st, tg: newTarget(t, ca.PublicKey()), agentKey: signer(t), bot: &posted{}, dir: t.TempDir()}
	g, err := New(Deps{Store: st, HostKey: signer(t), CA: ca, Dir: e.dir, Keep: 24 * time.Hour, Bot: e.bot, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	e.g = g
	if len(scopes) == 0 {
		scopes = []string{"status:read", "machines:ssh"}
	}
	if _, _, err := st.CreateToken("claude-code", scopes, 24*time.Hour, "test"); err != nil {
		t.Fatal(err)
	}
	e.giveKey(t, "claude-code", e.agentKey)
	m := store.Machine{Name: "box", Address: e.tg.addr, User: "beacon",
		HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(e.tg.hostKey.PublicKey())))}
	if err := st.PutMachine(m); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	e.addr = ln.Addr().String()
	go func() { _ = g.Serve(ln) }()
	return e
}

func (e *env) giveKey(t *testing.T, token string, k ssh.Signer) {
	t.Helper()
	if _, err := e.st.UpdateToken(token, func(tk *store.Token) error {
		tk.SSHKeys = append(tk.SSHKeys, store.SSHKey{Key: string(ssh.MarshalAuthorizedKey(k.PublicKey())), Fingerprint: ssh.FingerprintSHA256(k.PublicKey())})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) client(t *testing.T, machine string, key ssh.Signer) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", e.addr, &ssh.ClientConfig{User: machine, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.FixedHostKey(e.g.d.HostKey.PublicKey()), Timeout: 5 * time.Second})
}

// run runs a command through the channel as an agent's ssh would and returns stdout, stderr and the exit status.
func (e *env) run(t *testing.T, machine, cmd string) (string, string, int) {
	t.Helper()
	cl, err := e.client(t, machine, e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	s.Stdout, s.Stderr = &out, &errb
	code := 0
	if err := s.Run(cmd); err != nil {
		var ee *ssh.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run %q: %v", cmd, err)
		}
		code = ee.ExitStatus()
	}
	return out.String(), errb.String(), code
}

func waitSessions(t *testing.T, st *store.Store, n int) []store.SessionRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		all := st.Sessions(100, func(r store.SessionRecord) bool { return !r.Ended.IsZero() })
		if len(all) >= n || time.Now().After(deadline) {
			return all
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExec(t *testing.T) {
	needShell(t)
	e := setup(t)
	out, errOut, code := e.run(t, "box", "echo hello; echo oops >&2; exit 3")
	if out != "hello\n" || errOut != "oops\n" || code != 3 {
		t.Fatalf("got %q %q %d", out, errOut, code)
	}
	recs := waitSessions(t, e.st, 1)
	if len(recs) != 1 {
		t.Fatalf("sessions: %+v", recs)
	}
	r := recs[0]
	if r.Kind != "exec" || r.Agent != "claude-code" || r.Via != "ssh" || r.Machine != "box" || r.Exit == nil || *r.Exit != 3 {
		t.Errorf("record: %+v", r)
	}
	tr, err := e.g.Session(r.ID)
	if err != nil || tr.Stdout != "hello\n" || tr.Stderr != "oops\n" {
		t.Errorf("transcript: %+v %v", tr, err)
	}
	rc, err := e.g.Recording(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var hdr struct {
		Version int    `json:"version"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil || hdr.Version != 3 || !strings.Contains(hdr.Command, "echo hello") {
		t.Errorf("header %q: %v", lines[0], err)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, `"x","3"`) {
		t.Errorf("last event %q, want the exit status", last)
	}
	e.tg.mu.Lock()
	ids := strings.Join(e.tg.keyIDs, ";")
	e.tg.mu.Unlock()
	if !strings.Contains(ids, "for claude-code via ssh") {
		t.Errorf("certificate key ids %q", ids)
	}
	if a := e.st.AuditLog(5); len(a) == 0 || a[0].Action != "ssh_exec" || a[0].Target != "box" {
		t.Errorf("audit %+v", a)
	}
	e.g.echo.flush(context.Background(), true)
	if got := e.bot.all(); !strings.Contains(got, "claude-code on box:") || !strings.Contains(got, "(exit 3)") {
		t.Errorf("echo %q", got)
	}
}

func TestShell(t *testing.T) {
	needShell(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	e := setup(t)
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	in, _ := s.StdinPipe()
	var out syncBuffer
	s.Stdout, s.Stderr = &out, &out
	if err := s.Shell(); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"cd / && pwd", "false", "printf 'a\\nb\\n'", "exit"} {
		time.Sleep(300 * time.Millisecond)
		_, _ = in.Write([]byte(line + "\n"))
	}
	_ = s.Wait()
	if strings.Contains(out.String(), "\x1b]133") {
		t.Errorf("the marks reached the agent: %q", out.String())
	}
	recs := waitSessions(t, e.st, 1)
	tr, err := e.g.Session(recs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		cmd, out string
		exit     int
	}{{"cd / && pwd", "/", 0}, {"false", "", 1}, {"printf 'a\\nb\\n'", "a\nb", 0}}
	if len(tr.Commands) < len(want) {
		t.Fatalf("commands %+v (agent saw %q)", tr.Commands, out.String())
	}
	for i, w := range want {
		c := tr.Commands[i]
		if c.Command != w.cmd || c.Exit == nil || *c.Exit != w.exit || !strings.Contains(c.Output, w.out) {
			t.Errorf("command %d: %+v, want %+v", i, c, w)
		}
	}
	if tr.Kind != "shell" || recs[0].Commands < 3 {
		t.Errorf("record %+v", recs[0])
	}
}

// TestShellTypeAhead types every line before the first prompt, as ssh -tt fed from a pipe does: the
// shell still names each command.
func TestShellTypeAhead(t *testing.T) {
	needShell(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	e := setup(t)
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	s.Stdin = strings.NewReader("echo one\nls /nope\nexit\n")
	var out syncBuffer
	s.Stdout, s.Stderr = &out, &out
	if err := s.Shell(); err != nil {
		t.Fatal(err)
	}
	_ = s.Wait()
	recs := waitSessions(t, e.st, 1)
	tr, err := e.g.Session(recs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Commands) < 2 || tr.Commands[0].Command != "echo one" || tr.Commands[1].Command != "ls /nope" ||
		*tr.Commands[0].Exit != 0 || *tr.Commands[1].Exit == 0 {
		t.Errorf("commands %+v (agent saw %q)", tr.Commands, out.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRefusals(t *testing.T) {
	needShell(t)
	e := setup(t)
	_, errOut, code := e.run(t, "nowhere", "true")
	if code != 1 || !strings.Contains(errOut, "no such machine") || !strings.Contains(errOut, "box") {
		t.Errorf("unknown machine: %q %d", errOut, code)
	}
	if _, err := e.st.UpdateToken("claude-code", func(tk *store.Token) error { tk.Machines = []string{"other"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := e.run(t, "box", "true"); code != 1 || !strings.Contains(errOut, "may not reach") {
		t.Errorf("forbidden: %q %d", errOut, code)
	}
	if _, err := e.st.UpdateToken("claude-code", func(tk *store.Token) error { tk.Machines = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client(t, "box", signer(t)); err == nil {
		t.Error("an unknown key signed in")
	}
	if err := e.g.SetPaused(context.Background(), Actor{Name: "root", Via: "cli"}, true); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := e.run(t, "box", "true"); code != 1 || !strings.Contains(errOut, "paused") {
		t.Errorf("paused: %q %d", errOut, code)
	}
	if _, err := e.g.Run(context.Background(), Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}, "box", RunInput{Command: "true"}); !errors.Is(err, ErrPaused) {
		t.Errorf("paused tool: %v", err)
	}
	_ = e.g.SetPaused(context.Background(), Actor{Name: "root", Via: "cli"}, false)
	if _, _, code := e.run(t, "box", "true"); code != 0 {
		t.Errorf("resumed: exit %d", code)
	}
	if !strings.Contains(e.bot.all(), "paused agents' access") {
		t.Errorf("echo %q", e.bot.all())
	}
}

func TestNoScope(t *testing.T) {
	e := setup(t, "status:read")
	if _, err := e.client(t, "box", e.agentKey); err == nil {
		t.Error("a token without machines:ssh signed in")
	}
}

func TestTools(t *testing.T) {
	needShell(t)
	e := setup(t)
	ctx := context.Background()
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}

	r, err := e.g.Run(ctx, who, "box", RunInput{Command: "pwd; cat; exit 4", Cwd: "/", Stdin: "from stdin\n"})
	if err != nil || r.ExitCode != 4 || r.Stdout != "/\nfrom stdin\n" || r.Session == "" {
		t.Errorf("run: %+v %v", r, err)
	}
	r, err = e.g.Run(ctx, who, "box", RunInput{Command: "sleep 5", Timeout: 1})
	if err != nil || !r.TimedOut || r.ExitCode != -1 {
		t.Errorf("timeout: %+v %v", r, err)
	}

	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, "sub", "notes.txt"))
	w, err := e.g.WriteFile(ctx, who, "box", path, "one\ntwo\nthree\ntwo\n")
	if err != nil || w.Bytes != 18 {
		t.Fatalf("write: %+v %v", w, err)
	}
	v, err := e.g.ReadFile(ctx, who, "box", path, 2, 2)
	if err != nil || v.Content != "     2\ttwo\n     3\tthree\n" || v.TotalLines != 4 || !v.More || v.Lines != 2 {
		t.Errorf("read: %+v %v", v, err)
	}
	if _, err := e.g.ReadFile(ctx, who, "box", path+".missing", 0, 0); !errors.Is(err, ErrNoFile) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := e.g.EditFile(ctx, who, "box", EditInput{Path: path, OldString: "two", NewString: "2"}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("ambiguous edit: %v", err)
	}
	if _, err := e.g.EditFile(ctx, who, "box", EditInput{Path: path, OldString: "four", NewString: "4"}); !errors.Is(err, ErrNoMatch) {
		t.Errorf("no match: %v", err)
	}
	ed, err := e.g.EditFile(ctx, who, "box", EditInput{Path: path, OldString: "three", NewString: "3"})
	if err != nil || ed.Replacements != 1 {
		t.Errorf("edit: %+v %v", ed, err)
	}
	if _, err := e.g.EditFile(ctx, who, "box", EditInput{Path: path, OldString: "two", NewString: "2", ReplaceAll: true}); err != nil {
		t.Errorf("replace all: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "sub", "notes.txt"))
	if string(b) != "one\n2\n3\n2\n" {
		t.Errorf("file is %q", b)
	}
	e.g.echo.flush(ctx, true)
	got := e.bot.all()
	if !strings.Contains(got, "wrote "+path) || !strings.Contains(got, "edited "+path) || strings.Contains(got, "read ") {
		t.Errorf("echo %q", got)
	}
}

func TestAdd(t *testing.T) {
	needShell(t)
	e := setup(t)
	ctx := context.Background()
	who := Actor{Name: "root", Via: "cli"}
	a, err := e.g.Add(ctx, who, MachineInput{Name: "edge-1", Address: e.tg.addr})
	if err != nil {
		t.Fatal(err)
	}
	if a.Machine.HostKey != ssh.FingerprintSHA256(e.tg.hostKey.PublicKey()) || a.Machine.User != "beacon" || !strings.HasPrefix(a.TrustedUserCAKey, "ssh-ed25519 ") {
		t.Errorf("added %+v", a)
	}
	if _, err := e.g.Add(ctx, who, MachineInput{Name: "edge-1", Address: e.tg.addr}); !errors.Is(err, ErrExists) {
		t.Errorf("added twice: %v", err)
	}
	if _, _, code := e.run(t, "edge-1", "true"); code != 0 {
		t.Errorf("the new machine: exit %d", code)
	}
	l, _ := e.g.List(Actor{Name: "claude-code", Key: "token:claude-code"}, "beacon.example.com:17002")
	if len(l.Machines) != 2 || !l.Machines[1].Reachable || !strings.Contains(l.SSHConfig, "Host edge-1\n  HostName beacon.example.com\n  Port 17002\n  User edge-1") {
		t.Errorf("list %+v", l)
	}
	if err := e.g.Remove(ctx, who, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := e.g.Remove(ctx, who, "edge-1"); !errors.Is(err, ErrNoMachine) {
		t.Errorf("removed twice: %v", err)
	}
}

func TestShellLog(t *testing.T) {
	l := &shellLog{now: time.Now}
	feed := func(s string) string {
		var out []byte
		for i := 0; i < len(s); i += 3 { // in pieces, to cut marks apart
			out = append(out, l.output([]byte(s[i:min(i+3, len(s))]))...)
		}
		return string(out)
	}
	seen := feed("\x1b]133;D;0\a\x1b]133;A\a$ \x1b]133;B\a")
	l.input([]byte("ls /x\n"))
	seen += feed("ls /x\r\n\x1b]133;C\afile\r\n\x1b]133;D;2\a\x1b]133;A\a$ \x1b]133;B\a")
	seen += feed("$ \x1b]133;B\a") // a prompt redrawn after a resize
	l.input([]byte("ps"))          // typed ahead, or not at all: the shell names the command
	seen += feed("\x1b]133;C;psql\apsql> ")
	l.input([]byte("select 1;\n"))
	seen += feed("1\r\n\x1b]133;D;0\a")
	if strings.Contains(seen, "\x1b]133") {
		t.Errorf("marks leaked: %q", seen)
	}
	cs := l.done()
	if len(cs) != 2 || cs[0].Command != "ls /x" || *cs[0].Exit != 2 || cs[0].Output != "file\n" ||
		cs[1].Command != "psql" || len(cs[1].Inputs) != 1 || cs[1].Inputs[0] != "select 1;" {
		t.Errorf("commands %+v", cs)
	}
}

func TestClean(t *testing.T) {
	in := "\x1b[?2004h\x1b]0;title\a\x1b[01;32mgreen\x1b[00m\r\nprogress 10%\rprogress 100%\r\nab\bc\n"
	if got := clean(in); got != "green\nprogress 100%\nac\n" {
		t.Errorf("clean: %q", got)
	}
}

func TestPrune(t *testing.T) {
	needShell(t)
	e := setup(t)
	e.run(t, "box", "true")
	recs := waitSessions(t, e.st, 1)
	e.g.d.Keep = time.Nanosecond
	time.Sleep(time.Millisecond)
	e.g.prune()
	if len(e.st.Sessions(10, nil)) != 0 {
		t.Error("the session was kept")
	}
	if _, err := os.Stat(filepath.Join(e.dir, filepath.FromSlash(recs[0].Recording)+".cast")); !os.IsNotExist(err) {
		t.Errorf("the recording was kept: %v", err)
	}
}
