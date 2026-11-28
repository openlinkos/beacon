package sshgate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

// TestMachineIsOneRecord: no machine is added or registered again at another machine's address or with
// its host key, by anyone, the command line included; a connector that forwards another machine's sshd
// pins nothing.
func TestMachineIsOneRecord(t *testing.T) {
	e := setup(t)
	e.routes(map[string]route{
		"elsewhere.example.net:22": {to: e.tg.addr},  // box's sshd under another name
		"edge.example.net:22":      {to: e.nat.addr}, // a machine of its own
	})
	ctx := context.Background()
	cli := Actor{Name: "cli:root", Via: "cli"}
	wide := e.token(t, "wide", store.AllAccess())
	box, _ := e.st.Machine("box")
	_, port, _ := net.SplitHostPort(box.Address)

	for _, tc := range []struct {
		who  Actor
		addr string
	}{
		{wide, "elsewhere.example.net"},     // the same host key
		{cli, "elsewhere.example.net"},      // the command line too
		{cli, "[::ffff:127.0.0.1]:" + port}, // the same address, written otherwise
		{cli, "127.0.0.1:0" + port},         // and with a zero before the port
	} {
		_, err := e.g.Add(ctx, tc.who, MachineInput{Name: "alias", Address: tc.addr})
		if !errors.Is(err, ErrAlias) || !strings.Contains(err.Error(), "box") {
			t.Errorf("%s adds a second record of box at %s: %v", tc.who.Name, tc.addr, err)
		}
	}
	if _, ok := e.st.Machine("alias"); ok {
		t.Fatal("a second record of box was added")
	}
	// a machine of its own is added, and registered again in place
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "edge", Address: "edge.example.net"}); err != nil {
		t.Fatalf("a machine of its own: %v", err)
	}
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "edge", Address: "edge.example.net", Replace: true}); err != nil {
		t.Errorf("registering a machine again where it is: %v", err)
	}
	// nor is a machine registered again at another's address
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "edge", Address: "elsewhere.example.net", Replace: true}); !errors.Is(err, ErrAlias) {
		t.Errorf("edge registered again as box: %v", err)
	}

	// a connector machine that forwards box's sshd: its host key is not pinned, and it reaches nothing
	ck := signer(t)
	e.addConnector(t, "nat-alias", ck, "")
	e.connectOutTo(t, "nat-alias", ck, e.tg.serve)
	waitFor(t, "the connector", func() bool { return e.g.connectors.get("nat-alias") != nil })
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	if _, err := e.g.Run(ctx, who, "nat-alias", RunInput{Command: "true"}); err == nil || !strings.Contains(err.Error(), "box") {
		t.Errorf("through a connector that forwards box: %v", err)
	}
	if m, _ := e.st.Machine("nat-alias"); m.HostKey != "" {
		t.Errorf("a connector pinned box's host key: %+v", m)
	}
	refused := false
	for _, a := range e.st.AuditLog(20) {
		refused = refused || a.Action == "machine_host_key_refused" && a.Target == "nat-alias"
	}
	if !refused {
		t.Error("the refused host key is not audited")
	}
	// nor can it be given box's host key
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat-alias", Address: store.ConnectorAddress, Replace: true, HostKey: box.HostKey}); !errors.Is(err, ErrAlias) {
		t.Errorf("a connector machine given box's host key: %v", err)
	}
	// a host key of another kind than Ed25519 takes a person or the command line
	other := ecdsaKey(t)
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "nat-2", Address: store.ConnectorAddress, ConnectorKey: authorized(signer(t)), HostKey: other}); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "Ed25519") {
		t.Errorf("a credential gives an ECDSA host key: %v", err)
	}
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "nat-2", Address: store.ConnectorAddress, ConnectorKey: authorized(signer(t)), HostKey: authorized(signer(t))}); err != nil {
		t.Errorf("a credential gives an Ed25519 host key: %v", err)
	}
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat-3", Address: store.ConnectorAddress, ConnectorKey: authorized(signer(t)), HostKey: other}); err != nil {
		t.Errorf("the command line gives an ECDSA host key: %v", err)
	}
}

// TestResumeByPerson: a token pauses the channel and cannot resume it; a person, or beacon on the host,
// resumes it.
func TestResumeByPerson(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	wide := e.token(t, "wide", store.AllAccess())
	alice, bob := e.people(t)
	for _, who := range []Actor{alice, bob, {Name: "cli:root", Via: "cli"}} {
		if err := e.g.SetPaused(ctx, wide, true); err != nil || !e.g.Paused() {
			t.Fatalf("a token pauses: %v", err)
		}
		if err := e.g.SetPaused(ctx, wide, false); !errors.Is(err, ErrResumeByPerson) || !e.g.Paused() {
			t.Errorf("a token resumes: %v", err)
		}
		if err := e.g.SetPaused(ctx, who, false); err != nil || e.g.Paused() {
			t.Errorf("%s resumes: %v", who.Name, err)
		}
	}
}

func ecdsaKey(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
}

// TestNewMachinesAccountAndAddress: a new machine's account other than beacon takes a person or the
// command line, as a change of account does, so that removing a machine and adding it again changes
// nothing a change could not; and a machine on Beacon's own host, or its link, takes the command line,
// whatever its address is called.
func TestNewMachinesAccountAndAddress(t *testing.T) {
	e := setup(t)
	e.routes(map[string]route{
		"box.example.net:22":   {to: e.tg.addr},
		"edge.example.net:22":  {to: e.nat.addr},
		"local.example.net:22": {to: e.nat.addr, local: true}, // a name for a loopback address
	})
	ctx := context.Background()
	cli := Actor{Name: "cli:root", Via: "cli"}
	wide := e.token(t, "wide", store.AllAccess())
	_, bob := e.people(t)

	if err := e.g.Remove(ctx, wide, "box"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "box", Address: "box.example.net", User: "ops"}); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "person") {
		t.Errorf("a credential adds a machine reached as another account: %v", err)
	}
	if _, ok := e.st.Machine("box"); ok {
		t.Fatal("the refused machine was added")
	}
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "box", Address: "box.example.net"}); err != nil {
		t.Errorf("a credential adds a machine reached as beacon: %v", err)
	}
	if _, err := e.g.Add(ctx, bob, MachineInput{Name: "edge", Address: "edge.example.net", User: "ops"}); err != nil {
		t.Errorf("a person adds a machine reached as another account: %v", err)
	}

	for _, addr := range []string{"127.0.0.1", "127.8.0.1:2222", "[::1]:22", "localhost", "LocalHost.", "db.localhost", "0.0.0.0",
		"169.254.169.254", "[fe80::1]:22", "[::ffff:127.0.0.1]:22", "[::]:22", "local.example.net"} {
		for _, who := range []Actor{wide, bob} {
			if _, err := e.g.Add(ctx, who, MachineInput{Name: "self", Address: addr}); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "own address") {
				t.Errorf("%s adds a machine at %s: %v", who.Name, addr, err)
			}
		}
	}
	if _, ok := e.st.Machine("self"); ok {
		t.Fatal("a machine at a local address was added")
	}
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "self", Address: "box.example.net:0"}); !errors.Is(err, ErrBadInput) {
		t.Errorf("port 0: %v", err)
	}
	// the command line adds one there
	own := newTarget(t, e.g.d.CA.PublicKey())
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "self", Address: own.addr}); err != nil {
		t.Errorf("the command line adds a machine on this host: %v", err)
	}
}
