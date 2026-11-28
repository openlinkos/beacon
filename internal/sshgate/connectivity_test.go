package sshgate

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

func readinessSSHD(host ssh.Signer, ca ssh.PublicKey, channels *atomic.Int64) func(net.Conn) {
	cfg := &ssh.ServerConfig{NoClientAuth: ca == nil}
	if ca != nil {
		check := ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), ca.Marshal()) }}
		cfg.PublicKeyCallback = check.Authenticate
	}
	cfg.AddHostKey(host)
	return func(nc net.Conn) {
		defer func() { _ = nc.Close() }()
		_, incoming, requests, err := ssh.NewServerConn(nc, cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for ch := range incoming {
			channels.Add(1)
			_ = ch.Reject(ssh.Prohibited, "the test SSH service has no session or forwarding channels")
		}
	}
}

func readinessListen(t *testing.T, serve func(net.Conn)) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(nc)
		}
	}()
	return ln
}

func TestMachineSSHCheckDirect(t *testing.T) {
	e := setup(t)
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	host, other := signer(t), signer(t)
	trusted := e.g.d.CA.PublicKey()
	channels := &atomic.Int64{}
	for _, tt := range []struct {
		name  string
		pin   ssh.Signer
		ca    ssh.PublicKey
		state SSHCheckStatus
	}{
		{"ready", host, trusted, SSHCheckReady},
		{"rejected-certificate", host, other.PublicKey(), SSHCheckAuthenticationIncomplete},
		{"different-host-key", other, trusted, SSHCheckHostKeyMismatch},
		{"no-certificate-used", host, nil, SSHCheckAuthenticationSkipped},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ln := readinessListen(t, readinessSSHD(host, tt.ca, channels))
			machine := store.Machine{Name: tt.name, Address: ln.Addr().String(), User: "beacon", HostKey: authorized(tt.pin)}
			if err := e.st.PutMachine(machine); err != nil {
				t.Fatal(err)
			}
			// one machine, one record: the next case's machine pins the same host key
			t.Cleanup(func() { _ = e.st.DeleteMachine(machine.Name) })
			result, err := e.g.CheckSSH(t.Context(), who, machine.Name, "")
			if err != nil || result.Status != tt.state || result.Route != "direct" || result.Machine != tt.name ||
				result.CheckedAt.IsZero() || result.ID == "" {
				t.Fatalf("SSH check = %+v, %v", result, err)
			}
			audit := e.st.AuditLog(2)
			if len(audit) != 2 || audit[0].Action != "machine_ssh_check" || audit[0].Target != tt.name ||
				audit[0].Via != "mcp" || audit[0].Actor != who.Name || audit[0].Detail != result.ID+" "+string(tt.state) ||
				audit[1].Detail != result.ID+" started" {
				t.Fatalf("audit = %+v", audit)
			}
		})
	}
	if channels.Load() != 0 || len(e.st.Sessions(1, nil)) != 0 || e.recordings() != "" {
		t.Fatal("the check opened a remote channel or created session evidence")
	}
	e.g.echo.flush(t.Context(), true)
	if !strings.Contains(e.bot.all(), "verified the machine's host key and SSH certificate login") {
		t.Fatalf("missing audit chat echo: %s", e.bot.all())
	}
}

func TestMachineSSHCheckNAT(t *testing.T) {
	e := setup(t)
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	connectorKey := signer(t)
	e.addConnector(t, "nat-box", connectorKey, authorized(e.nat.hostKey))
	out, err := e.g.CheckSSH(t.Context(), who, "nat-box", "")
	if err != nil || out.Status != SSHCheckDisconnected || out.Route != "connector" {
		t.Fatalf("offline connector: %+v, %v", out, err)
	}
	channels := &atomic.Int64{}
	cl := e.connectOutTo(t, "nat-box", connectorKey, readinessSSHD(e.nat.hostKey, e.g.d.CA.PublicKey(), channels))
	out, err = e.g.CheckSSH(t.Context(), who, "nat-box", "")
	if err != nil || out.Status != SSHCheckReady || out.Route != "connector" || channels.Load() != 0 {
		t.Fatalf("connected machine: %+v, %v, downstream channels %d", out, err, channels.Load())
	}
	_ = cl.Close()
	waitFor(t, "the connector to disconnect", func() bool { return e.g.connectors.get("nat-box") == nil })
}

func TestMachineSSHCheckStopsAtDeadlineWithoutClosingConnector(t *testing.T) {
	e := setup(t)
	who := Actor{Name: "claude-code", Via: "api", Key: "token:claude-code"}
	connectorKey := signer(t)
	e.addConnector(t, "nat-box", connectorKey, authorized(e.nat.hostKey))
	cl, err := e.client(t, "nat-box", connectorKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	incoming := cl.HandleChannelOpen("forwarded-tcpip")
	ok, _, err := cl.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Addr string
		Port uint32
	}{"127.0.0.1", 22}))
	if err != nil || !ok {
		t.Fatalf("connector forward registration: %v, %v", ok, err)
	}
	type attempt struct {
		out SSHCheck
		err error
	}
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	done := make(chan attempt, 1)
	go func() {
		out, err := e.g.CheckSSH(ctx, who, "nat-box", "")
		done <- attempt{out, err}
	}()
	var pending ssh.NewChannel
	select {
	case pending = <-incoming:
	case <-time.After(testWait):
		t.Fatal("the connector saw no channel open")
	}
	select {
	case check := <-done:
		if check.err != nil || check.out.Status != SSHCheckTimedOut {
			t.Fatalf("stalled connector: %+v, %v", check.out, check.err)
		}
	case <-time.After(testWait):
		t.Fatal("the readiness check outlived the deadline without replying")
	}
	out, err := e.g.CheckSSH(t.Context(), who, "nat-box", "")
	if err != nil || out.Status != SSHCheckConnectorBusy {
		t.Fatalf("retry while connector has an outstanding check: %+v, %v", out, err)
	}
	if err := pending.Reject(ssh.ConnectionFailed, "the test SSH server refused the connection"); err != nil {
		t.Fatalf("the shared connector closed with the check: %v", err)
	}
	waitFor(t, "the outstanding check to close", func() bool {
		c := e.g.connectors.get("nat-box")
		return c != nil && !c.checking.Load()
	})
}

func TestMachineSSHCheckAdmissionAndRevocation(t *testing.T) {
	e := setup(t)
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	dials := &atomic.Int64{}
	checking := make(chan struct{})
	e.g.d.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials.Add(1)
		close(checking)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := e.g.CheckSSH(t.Context(), who, "box", "a-stale-registration"); !errors.Is(err, ErrMachineChanged) {
		t.Fatalf("stale enrolment should fail: %v", err)
	}
	e.g.SetLeader(func() bool { return false })
	if _, err := e.g.CheckSSH(t.Context(), who, "box", ""); !errors.Is(err, ErrStandby) {
		t.Fatalf("standby should fail: %v", err)
	}
	e.g.SetLeader(nil)
	if err := e.g.SetPaused(t.Context(), who, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.CheckSSH(t.Context(), who, "box", ""); !errors.Is(err, ErrPaused) {
		t.Fatalf("paused channel should fail: %v", err)
	}
	if dials.Load() != 0 {
		t.Fatal("a refused check reached the upstream machine")
	}
	if err := e.g.SetPaused(t.Context(), Actor{Name: "cli:root", Via: "cli"}, false); err != nil { // a person or the command line resumes
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.g.CheckSSH(t.Context(), who, "box", "")
		done <- err
	}()
	select {
	case <-checking:
	case <-time.After(testWait):
		t.Fatal("no authorised dial")
	}
	if err := e.st.RevokeToken("claude-code"); err != nil {
		t.Fatal(err)
	}
	e.g.Recheck()
	select {
	case err := <-done:
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("revocation must prevent a check from returning a result: %v", err)
		}
	case <-time.After(testWait):
		t.Fatal("revocation did not interrupt the upstream connection")
	}
	if audit := e.st.AuditLog(1); len(audit) != 1 || !strings.HasSuffix(audit[0].Detail, " interrupted") {
		t.Fatalf("revoked check audit = %+v", audit)
	}
}
