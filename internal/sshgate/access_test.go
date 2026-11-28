package sshgate

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

// TestOpenConnectionsFollowTheirMachine: a connection, and every channel it carries, stays bound to the
// machine as it was when it opened. Another account, a removal or a new enrolment closes it.
func TestOpenConnectionsFollowTheirMachine(t *testing.T) {
	needShell(t)
	cli := Actor{Name: "root", Via: "cli"}
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, e *env)
	}{
		{"account", func(t *testing.T, e *env) {
			user := "ops"
			if _, err := e.g.Update(context.Background(), cli, "box", MachineUpdate{User: &user}); err != nil {
				t.Fatal(err)
			}
		}},
		{"restored settings", func(t *testing.T, e *env) {
			m, _ := e.st.Machine("box")
			user := "ops"
			if _, _, err := e.st.PatchMachineMetadata("box", store.MachineMetadataUpdate{User: &user}, 0, "op", "api"); err != nil {
				t.Fatal(err)
			}
			cur, _ := e.st.Machine("box")
			e.g.Recheck() // the account changed outside the channel: closed, as by the minute's check
			e.waitOpen(t, 0)
			done := e.open(t, e.agentKey)
			if _, err := e.g.RestoreMachine(context.Background(), cli, "box", MachineRestore{Version: m.MetadataVersion, IfVersion: cur.MetadataVersion}); err != nil {
				t.Fatal(err)
			}
			ended(t, done, "restoring the earlier account")
		}},
		{"removed", func(t *testing.T, e *env) {
			if err := e.g.Remove(context.Background(), cli, "box"); err != nil {
				t.Fatal(err)
			}
		}},
		{"enrolled again", func(t *testing.T, e *env) {
			if _, err := e.g.Add(context.Background(), cli, MachineInput{Name: "box", Address: e.tg.addr, Replace: true}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			done := e.open(t, e.agentKey)
			tc.change(t, e)
			ended(t, done, tc.name)
		})
	}
}

// TestChannelsRefuseAChangedMachine: a multiplexed connection opens no new channel on a machine that is no
// longer the one it connected to, even before anything closes it.
func TestChannelsRefuseAChangedMachine(t *testing.T) {
	needShell(t)
	e := setup(t)
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	runOn := func(cmd string) (string, error) {
		s, err := cl.NewSession()
		if err != nil {
			return "", err
		}
		var stderr bytes.Buffer
		s.Stderr = &stderr
		err = s.Run(cmd)
		return stderr.String(), err
	}
	if _, err := runOn("true"); err != nil {
		t.Fatalf("first session: %v", err)
	}
	user := "ops" // changed as another instance would change it: nothing here closes the connection
	if _, _, err := e.st.PatchMachineMetadata("box", store.MachineMetadataUpdate{User: &user}, 0, "op", "api"); err != nil {
		t.Fatal(err)
	}
	stderr, err := runOn("true")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 1 || !strings.Contains(stderr, ErrMachineChanged.Error()) {
		t.Fatalf("a channel reused the connection to the earlier machine: %v %q", err, stderr)
	}
	e.tg.mu.Lock()
	certs := len(e.tg.keyIDs)
	e.tg.mu.Unlock()
	if certs != 1 {
		t.Fatalf("the machine saw %d certificates", certs)
	}
	if !audited(e.st, "ssh_refused", "box", "machine changed") {
		t.Fatalf("the refusal was not audited: %+v", e.st.AuditLog(10))
	}
}

// TestServingEndsWithItsContext: when the instance stops serving the channel (it lost the HA lease),
// the listener, the connections and the tool calls it has open all close.
func TestServingEndsWithItsContext(t *testing.T) {
	needShell(t)
	e := setup(t)
	var leader atomic.Bool
	leader.Store(true)
	e.g.SetLeader(leader.Load)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, lose := context.WithCancel(context.Background())
	defer lose()
	served := make(chan error, 1)
	go func() { served <- e.g.Serve(ctx, ln) }()
	cfg := &ssh.ClientConfig{User: "box", Auth: []ssh.AuthMethod{ssh.PublicKeys(e.agentKey)},
		HostKeyCallback: ssh.FixedHostKey(e.g.d.HostKey.PublicKey()), Timeout: 5 * time.Second}
	cl, err := ssh.Dial("tcp", ln.Addr().String(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("sleep 30"); err != nil {
		t.Fatal(err)
	}
	tool := make(chan error, 1)
	go func() {
		_, err := e.g.Run(context.Background(), toolActor, "box", RunInput{Command: "sleep 30", Timeout: 60})
		tool <- err
	}()
	e.waitOpen(t, 2)

	leader.Store(false)
	lose()
	if !closed(cl) {
		t.Fatal("the connection outlived the lease")
	}
	select {
	case err := <-tool:
		if !errors.Is(err, ErrStandby) {
			t.Fatalf("the tool call ended with %v", err)
		}
	case <-time.After(testWait):
		t.Fatal("the tool call outlived the lease")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testWait):
		t.Fatal("Serve did not return")
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the listener outlived the lease")
	}
	e.waitOpen(t, 0)
}

// TestRecheckClosesWithoutTheLease: what is open closes at the next check once the instance is the standby.
func TestRecheckClosesWithoutTheLease(t *testing.T) {
	needShell(t)
	e := setup(t)
	var leader atomic.Bool
	leader.Store(true)
	e.g.SetLeader(leader.Load)
	done := e.open(t, e.agentKey)
	leader.Store(false)
	if n := e.g.Recheck(); n != 1 {
		t.Fatalf("Recheck closed %d", n)
	}
	ended(t, done, "losing the lease")
}

// TestRecheckFollowsThePauseSwitch: the pause switch is part of every check, so a pause recorded by any
// means closes what is open at the next check, and a tool call says it was paused, not that it timed out.
func TestRecheckFollowsThePauseSwitch(t *testing.T) {
	needShell(t)
	e := setup(t)
	done := e.open(t, e.agentKey)
	tool := make(chan error, 1)
	go func() {
		_, err := e.g.Run(context.Background(), toolActor, "box", RunInput{Command: "sleep 30", Timeout: 60})
		tool <- err
	}()
	e.waitOpen(t, 2)
	if err := e.st.SetTime(PauseKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := e.g.Recheck(); n != 2 {
		t.Fatalf("Recheck closed %d", n)
	}
	ended(t, done, "the pause")
	select {
	case err := <-tool:
		if !errors.Is(err, ErrPaused) {
			t.Fatalf("the tool call ended with %v", err)
		}
	case <-time.After(testWait):
		t.Fatal("the tool call outlived the pause")
	}
}

// TestEditChecksAgainBeforeItWrites: a pause that comes while an edit reads the file stops its write.
func TestEditChecksAgainBeforeItWrites(t *testing.T) {
	needShell(t)
	e := setup(t)
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "notes.txt"))
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	dial := e.g.d.Dial
	e.g.d.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) == 1 { // the read: the pause lands while it runs
			if err := e.st.SetTime(PauseKey, time.Now()); err != nil {
				return nil, err
			}
		}
		return dial(ctx, network, addr)
	}
	if _, err := e.g.EditFile(context.Background(), toolActor, "box", EditInput{Path: path, OldString: "one", NewString: "two"}); !errors.Is(err, ErrPaused) {
		t.Fatalf("edit: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "one\n" || dials.Load() != 1 {
		t.Fatalf("the edit wrote %q after the pause (%d dials)", b, dials.Load())
	}
}
