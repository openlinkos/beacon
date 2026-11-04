package sshgate

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

func authorized(k ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.PublicKey())))
}

// connectOut is a machine's connector for the tests: it signs in as the machine with its connector key
// and asks for a remote forward, as ssh -R does (the x/crypto client's Listen sends tcpip-forward and
// accepts forwarded-tcpip channels), handing every connection Beacon opens to the test machine's sshd.
func (e *env) connectOut(t *testing.T, machine string, key ssh.Signer) *ssh.Client {
	t.Helper()
	cl, err := e.client(t, machine, key)
	if err != nil {
		t.Fatalf("connector sign-in: %v", err)
	}
	ln, err := cl.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = cl.Close()
		t.Fatalf("remote forward: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go e.tg.serve(c)
		}
	}()
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// addConnector adds a connector machine called name with key, without a host key unless hostKey is set.
func (e *env) addConnector(t *testing.T, name string, key ssh.Signer, hostKey string) Added {
	t.Helper()
	a, err := e.g.Add(context.Background(), Actor{Name: "root", Via: "cli"},
		MachineInput{Name: name, Address: store.ConnectorAddress, ConnectorKey: authorized(key), HostKey: hostKey})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited in vain for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// closed reports whether the client's connection ends within a few seconds.
func closed(cl *ssh.Client) bool {
	done := make(chan struct{})
	go func() { _ = cl.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func TestConnectorLearnsHostKey(t *testing.T) {
	e := setup(t)
	e.g.d.PublicAddress = "ssh.example.com:17002"
	ck := signer(t)
	a := e.addConnector(t, "nat-box", ck, "")
	if !a.Machine.Connector || a.Machine.Connected == nil || *a.Machine.Connected || a.Machine.HostKey != "" || a.Connector == nil || !a.Connector.HostKeyPending {
		t.Fatalf("added %+v %+v", a.Machine, a.Connector)
	}
	if s := a.Connector; !strings.HasPrefix(s.KnownHosts, "[ssh.example.com]:17002 ssh-ed25519 ") ||
		!strings.Contains(s.Command, "-R 0:localhost:22 -p 17002") || !strings.HasSuffix(s.Command, " nat-box@ssh.example.com") ||
		!strings.Contains(s.Unit, "Restart=always") || !strings.Contains(s.Script, s.KnownHosts) || !strings.Contains(s.Script, "TrustedUserCAKeys") {
		t.Errorf("setup %+v", s)
	}
	ctx := context.Background()
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	if _, err := e.g.Run(ctx, who, "nat-box", RunInput{Command: "true"}); !errors.Is(err, ErrDisconnected) {
		t.Errorf("disconnected: %v", err)
	}

	cl := e.connectOut(t, "nat-box", ck)
	waitFor(t, "the host key to be learned", func() bool { return strings.Contains(e.bot.all(), "pinned the host key of machine nat-box") })
	m, _ := e.st.Machine("nat-box")
	if m.HostKey != authorized(e.tg.hostKey) {
		t.Errorf("learned %q, want the machine's host key", m.HostKey)
	}
	if a := e.st.AuditLog(5); len(a) == 0 || a[0].Action != "machine_host_key" || a[0].Target != "nat-box" || a[0].Detail != ssh.FingerprintSHA256(e.tg.hostKey.PublicKey()) {
		t.Errorf("audit %+v", a)
	}
	l, _ := e.g.List(Actor{Name: "claude-code", Key: "token:claude-code"}, "")
	var v MachineView
	for _, x := range l.Machines {
		if x.Name == "nat-box" {
			v = x
		}
	}
	if !v.Connector || v.Connected == nil || !*v.Connected || v.ConnectedSince.IsZero() || v.HostKey == "" || v.Address != "connector" {
		t.Errorf("list %+v", v)
	}

	// the newest connection wins: the first is closed, the machine stays reachable
	cl2 := e.connectOut(t, "nat-box", ck)
	if !closed(cl) {
		t.Error("the older connector connection stayed open")
	}
	if c := e.g.connectors.get("nat-box"); c == nil {
		t.Fatal("no connector after the second connection")
	}
	// a host key can be given rather than learned, and replace keeps the key
	a2, err := e.g.Add(ctx, Actor{Name: "root", Via: "cli"}, MachineInput{Name: "nat-box", Address: "connector", Replace: true, HostKey: authorized(e.tg.hostKey)})
	if err != nil || a2.Connector.HostKeyPending || a2.Machine.HostKey != ssh.FingerprintSHA256(e.tg.hostKey.PublicKey()) {
		t.Errorf("replace with a host key: %+v %v", a2, err)
	}
	if m, _ := e.st.Machine("nat-box"); m.ConnectorFingerprint != ssh.FingerprintSHA256(ck.PublicKey()) {
		t.Errorf("replace lost the connector key: %+v", m)
	}
	// removing the machine closes its connector
	if err := e.g.Remove(ctx, Actor{Name: "root", Via: "cli"}, "nat-box"); err != nil {
		t.Fatal(err)
	}
	if !closed(cl2) {
		t.Error("the connector of a removed machine stayed connected")
	}
}

func TestConnectorRefusals(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	root := Actor{Name: "root", Via: "cli"}
	ck := signer(t)
	e.addConnector(t, "nat-box", ck, authorized(e.tg.hostKey))

	// a connector connection carries no channels
	cl := e.connectOut(t, "nat-box", ck)
	if _, err := cl.NewSession(); err == nil {
		t.Error("a connector opened a session")
	}
	if _, err := cl.Dial("tcp", "127.0.0.1:1"); err == nil {
		t.Error("a connector opened a forwarded port")
	}
	// a token's key registers no forward
	agent, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if _, err := agent.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Error("an agent registered a remote forward")
	}
	// a connector key signs in only as its own machine, and never holds a token's key
	if _, err := e.client(t, "box", ck); err == nil {
		t.Error("a connector key signed in as another machine")
	}
	if _, err := e.g.SetConnector(ctx, root, "nat-box", authorized(e.agentKey)); !errors.Is(err, ErrBadInput) {
		t.Errorf("a token's key as connector key: %v", err)
	}
	if _, err := e.g.Add(ctx, root, MachineInput{Name: "nat-2", Address: "connector", ConnectorKey: authorized(e.agentKey)}); !errors.Is(err, ErrBadInput) {
		t.Errorf("a token's key as connector key at add: %v", err)
	}
	if _, err := e.g.Add(ctx, root, MachineInput{Name: "nat-2", Address: e.tg.addr, ConnectorKey: authorized(ck)}); !errors.Is(err, ErrBadInput) {
		t.Errorf("a connector key on a dialled machine: %v", err)
	}
	if _, err := e.g.Add(ctx, root, MachineInput{Name: "nat-box", Address: "connector"}); !errors.Is(err, ErrExists) {
		t.Errorf("added twice: %v", err)
	}
	if _, err := e.g.SetConnector(ctx, root, "box", authorized(signer(t))); !errors.Is(err, ErrBadInput) {
		t.Errorf("a connector key on a dialled machine: %v", err)
	}
	if _, err := e.g.SetConnector(ctx, root, "nowhere", authorized(signer(t))); !errors.Is(err, ErrNoMachine) {
		t.Errorf("unknown machine: %v", err)
	}
	// a new key ends the connection made with the old one; the old key no longer signs in
	nk := signer(t)
	v, err := e.g.SetConnector(ctx, root, "nat-box", authorized(nk))
	if err != nil || !v.Connector {
		t.Fatalf("set connector: %+v %v", v, err)
	}
	if !closed(cl) {
		t.Error("the connection with the old key stayed open")
	}
	if _, err := e.client(t, "nat-box", ck); err == nil {
		t.Error("the old connector key still signs in")
	}
	if a := e.st.AuditLog(1); len(a) != 1 || a[0].Action != "machine_connector" || a[0].Detail != ssh.FingerprintSHA256(nk.PublicKey()) {
		t.Errorf("audit %+v", a)
	}
	if !strings.Contains(e.bot.all(), "root set the connector key of machine nat-box") {
		t.Errorf("echo %q", e.bot.all())
	}
	cl2 := e.connectOut(t, "nat-box", nk)
	defer cl2.Close()
	l, _ := e.g.List(Actor{Name: "claude-code", Key: "token:claude-code"}, "")
	for _, m := range l.Machines {
		if m.Name == "nat-box" && (m.Connected == nil || !*m.Connected) {
			t.Errorf("not connected with the new key: %+v", m)
		}
		if m.Name == "box" && (m.Connector || m.Connected != nil) {
			t.Errorf("a dialled machine with connector fields: %+v", m)
		}
	}
}

func TestConnectorSessions(t *testing.T) {
	needShell(t)
	e := setup(t)
	e.g.d.SourceAddresses = []string{"127.0.0.0/8"} // the test machines see every connection come from loopback
	ctx := context.Background()
	ck := signer(t)
	e.addConnector(t, "nat-box", ck, "")
	if _, errOut, code := e.run(t, "nat-box", "true"); code != 1 || !strings.Contains(errOut, "connector is not connected") {
		t.Errorf("disconnected over ssh: %q %d", errOut, code)
	}
	e.connectOut(t, "nat-box", ck)

	// a command over the agent's own ssh, relayed through the connector
	if out, errOut, code := e.run(t, "nat-box", "echo via nat; echo oops >&2; exit 3"); out != "via nat\n" || errOut != "oops\n" || code != 3 {
		t.Errorf("exec: %q %q %d", out, errOut, code)
	}
	recs := waitSessions(t, e.st, 1)
	if len(recs) != 1 || recs[0].Machine != "nat-box" || recs[0].Kind != "exec" || *recs[0].Exit != 3 {
		t.Errorf("record %+v", recs)
	}
	// the tools
	r, err := e.g.Run(ctx, Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}, "nat-box", RunInput{Command: "cat", Stdin: "tooled\n"})
	if err != nil || r.ExitCode != 0 || r.Stdout != "tooled\n" {
		t.Errorf("run tool: %+v %v", r, err)
	}
	// only a connector machine's certificate allows the loopback source
	if _, _, code := e.run(t, "box", "true"); code != 0 {
		t.Errorf("box: exit %d", code)
	}
	e.tg.mu.Lock()
	sources := strings.Join(e.tg.sources, ";")
	e.tg.mu.Unlock()
	if !strings.Contains(sources, "127.0.0.0/8,127.0.0.1/32,::1/128") || !strings.HasSuffix(sources, ";127.0.0.0/8") {
		t.Errorf("certificate sources %q", sources)
	}
	// an interactive shell
	if _, err := exec.LookPath("bash"); err == nil {
		cl, err := e.client(t, "nat-box", e.agentKey)
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
		s.Stdin = strings.NewReader("echo shell-via-nat\nexit\n")
		var out syncBuffer
		s.Stdout, s.Stderr = &out, &out
		if err := s.Shell(); err != nil {
			t.Fatal(err)
		}
		_ = s.Wait()
		recs := waitSessions(t, e.st, 4)
		var shell *store.SessionRecord
		for i := range recs {
			if recs[i].Kind == "shell" {
				shell = &recs[i]
			}
		}
		if shell == nil {
			t.Fatalf("no shell session: %+v (agent saw %q)", recs, out.String())
		}
		tr, _ := e.g.Session(shell.ID)
		if len(tr.Commands) < 1 || tr.Commands[0].Command != "echo shell-via-nat" {
			t.Errorf("shell commands %+v (agent saw %q)", tr.Commands, out.String())
		}
	}
	// the pause switch closes relayed sessions; the machine's own connection stays
	cl, err := e.client(t, "nat-box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("sleep 30"); err != nil {
		t.Fatal(err)
	}
	if err := e.g.SetPaused(ctx, Actor{Name: "root", Via: "cli"}, true); err != nil {
		t.Fatal(err)
	}
	if !closed(cl) {
		t.Error("the agent's connection survived the pause")
	}
	if c := e.g.connectors.get("nat-box"); c == nil {
		t.Error("the pause disconnected the machine's connector")
	}
	_ = e.g.SetPaused(ctx, Actor{Name: "root", Via: "cli"}, false)
	if _, _, code := e.run(t, "nat-box", "true"); code != 0 {
		t.Errorf("after resume: exit %d", code)
	}
}
