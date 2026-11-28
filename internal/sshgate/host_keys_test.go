package sshgate

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

// TestMachineKnownByEveryHostKey: a machine is known by every host key it offers, not only the one Beacon
// pins. No second record is made for it with another of its keys, whether Beacon dials it or reaches it
// through a connector, and a machine registered now keeps all its keys on record for later comparison.
func TestMachineKnownByEveryHostKey(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ca := e.g.d.CA.PublicKey()
	cli := Actor{Name: "cli:root", Via: "cli"}
	wide := e.token(t, "wide", store.AllAccess())

	// vault, critical, was registered with its ECDSA key alone; its sshd has an Ed25519 key as well
	vaultEC := ecdsaSigner(t)
	vault := newTargetKeys(t, ca, signer(t), vaultEC)
	web := newTargetKeys(t, ca, signer(t), ecdsaSigner(t))
	e.routes(map[string]route{
		"vault.example.net:22":   {to: vault.addr},
		"vault-2.example.net:22": {to: vault.addr}, // vault's sshd under another name
		"web.example.net:22":     {to: web.addr},
	})
	if err := e.st.PutMachine(store.Machine{Name: "vault", Address: "vault.example.net:22", User: "beacon", HostKey: authorized(vaultEC), Critical: true}); err != nil {
		t.Fatal(err)
	}
	for _, who := range []Actor{wide, cli} {
		if _, err := e.g.Add(ctx, who, MachineInput{Name: "alias", Address: "vault-2.example.net"}); !errors.Is(err, ErrAlias) || !strings.Contains(err.Error(), "vault") {
			t.Errorf("%s adds vault again under another name, pinning its other key: %v", who.Name, err)
		}
	}
	if _, ok := e.st.Machine("alias"); ok {
		t.Fatal("a second record of vault was added")
	}

	// a machine registered now is known by all its keys, and audited with them
	a, err := e.g.Add(ctx, wide, MachineInput{Name: "web", Address: "web.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := e.st.Machine("web")
	keys := m.KnownHostKeys()
	if a.Machine.HostKey != ssh.FingerprintSHA256(web.hostKey.PublicKey()) || len(keys) != 2 || keys[0] != authorized(web.hostKey) || !strings.HasPrefix(keys[1], "ecdsa-sha2-nistp256 ") {
		t.Fatalf("web is known by %q, pinned %s", keys, a.Machine.HostKey)
	}
	if !audited(e.st, "machine_add", "web", fingerprint(keys[0])+" "+fingerprint(keys[1])) {
		t.Error("web's keys are not in the audit log")
	}
	// so a record given web's other key is refused, the command line's included
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat-web", Address: store.ConnectorAddress, ConnectorKey: authorized(signer(t)), HostKey: keys[1]}); !errors.Is(err, ErrAlias) || !strings.Contains(err.Error(), "web") {
		t.Errorf("a connector machine given web's ECDSA key: %v", err)
	}
	// and registering web again where it is still works
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "web", Address: "web.example.net", Replace: true}); err != nil {
		t.Errorf("web registered again in place: %v", err)
	}

	// a connector that forwards vault's sshd pins nothing: Beacon asks it for every key
	ck := signer(t)
	e.addConnector(t, "nat-vault", ck, "")
	e.connectOutTo(t, "nat-vault", ck, vault.serve)
	waitFor(t, "the refusal", func() bool { return audited(e.st, "machine_host_key_refused", "nat-vault", "vault") })
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	if _, err := e.g.Run(ctx, who, "nat-vault", RunInput{Command: "true"}); err == nil || !strings.Contains(err.Error(), "vault") {
		t.Errorf("through a connector that forwards vault: %v", err)
	}
	if m, _ := e.st.Machine("nat-vault"); m.HostKey != "" || m.HostKeys != "" {
		t.Errorf("a connector recorded vault's keys: %+v", m)
	}

	// through a connector Beacon pins only an Ed25519 key; another kind is given by a person or the
	// command line, and recorded with the machine's other keys once the connector carries the question
	ecOnlyKey := ecdsaSigner(t)
	ecOnly := newTargetKeys(t, ca, ecOnlyKey)
	ek := signer(t)
	e.addConnector(t, "nat-ec", ek, "")
	ecl := e.connectOutTo(t, "nat-ec", ek, ecOnly.serve)
	waitFor(t, "the refusal", func() bool { return audited(e.st, "machine_host_key_refused", "nat-ec", "Ed25519") })
	_ = ecl.Close()
	waitFor(t, "the connector to go", func() bool { return e.g.connectors.get("nat-ec") == nil })
	if m, _ := e.st.Machine("nat-ec"); m.HostKey != "" {
		t.Errorf("an ECDSA key was pinned through a connector: %+v", m)
	}
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat-ec", Address: store.ConnectorAddress, Replace: true, HostKey: authorized(ecOnlyKey)}); err != nil {
		t.Fatal(err)
	}
	e.connectOutTo(t, "nat-ec", ek, ecOnly.serve)
	waitFor(t, "a connection to the machine", func() bool { // the first asks for its host keys
		m, _ := e.st.Machine("nat-ec")
		nc, err := e.g.connect(ctx, &m)
		if err == nil {
			_ = nc.Close()
		}
		return err == nil
	})
	if m, _ := e.st.Machine("nat-ec"); m.HostKey != authorized(ecOnlyKey) || m.HostKeys != authorized(ecOnlyKey) {
		t.Errorf("the given key, confirmed: %+v", m)
	}
	if !audited(e.st, "machine_host_key", "nat-ec", ssh.FingerprintSHA256(ecOnlyKey.PublicKey())) {
		t.Error("the recorded host keys are not in the audit log")
	}
	// a given key the machine does not offer has nothing recorded, and reaches nothing
	gk := signer(t)
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat-given", Address: store.ConnectorAddress, ConnectorKey: authorized(gk), HostKey: authorized(signer(t))}); err != nil {
		t.Fatal(err)
	}
	e.connectOutTo(t, "nat-given", gk, e.nat.serve)
	waitFor(t, "the refusal", func() bool {
		m, _ := e.st.Machine("nat-given")
		if nc, err := e.g.connect(ctx, &m); err == nil {
			_ = nc.Close()
		}
		return audited(e.st, "machine_host_key_refused", "nat-given", "does not offer the host key pinned")
	})
	if m, _ := e.st.Machine("nat-given"); m.HostKeys != "" {
		t.Errorf("keys recorded for a machine that does not offer its pinned key: %+v", m)
	}

	// the start-up check compares every key on record
	if err := e.st.PutMachine(store.Machine{Name: "web-old", Address: "192.0.2.80:22", User: "beacon", HostKey: keys[1]}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, alias := range e.st.MachineAliases() {
		found = found || strings.HasPrefix(alias, "web and web-old: ")
	}
	if !found {
		t.Errorf("aliases %q", e.st.MachineAliases())
	}
}

// reachedAt is a connection to a test machine that says it reached the address given.
type reachedAt struct {
	net.Conn
	at net.IP
}

func (c reachedAt) RemoteAddr() net.Addr { return &net.TCPAddr{IP: c.at, Port: 22} }

// TestHostKeysFromOnePlace: a name that leads to another address while Beacon asks for its host keys is
// refused, so that every key comes from the machine registered.
func TestHostKeysFromOnePlace(t *testing.T) {
	e := setup(t)
	var n atomic.Int32
	var d net.Dialer
	e.g.d.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		to, at := e.tg.addr, net.IPv4(192, 0, 2, 10)
		if n.Add(1)%2 == 0 {
			to, at = e.nat.addr, net.IPv4(192, 0, 2, 11)
		}
		nc, err := d.DialContext(ctx, network, to)
		if err != nil {
			return nil, err
		}
		return reachedAt{nc, at}, nil
	}
	wide := e.token(t, "wide", store.AllAccess())
	if _, err := e.g.Add(context.Background(), wide, MachineInput{Name: "flip", Address: "flip.example.net"}); !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "192.0.2.11") {
		t.Errorf("a name that led to two machines: %v", err)
	}
	if _, ok := e.st.Machine("flip"); ok {
		t.Error("the machine was added")
	}
}

// TestMachineWritesNeedTheirRecord: while audit entries cannot be written, no machine is added or
// registered again, no connector key is set and no host key is pinned through a connector; once they can,
// each is made with its entry.
func TestMachineWritesNeedTheirRecord(t *testing.T) {
	e := setup(t)
	edge := newTarget(t, e.g.d.CA.PublicKey())
	e.routes(map[string]route{"edge.example.net:22": {to: edge.addr}})
	ctx := context.Background()
	cli := Actor{Name: "cli:root", Via: "cli"}
	ck := signer(t)
	e.addConnector(t, "nat-rec", ck, "")
	box, _ := e.st.Machine("box")

	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test write rejection'); END"); err != nil {
		t.Fatal(err)
	}
	echoes := e.bot.all()

	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "edge", Address: "edge.example.net"}); !errors.Is(err, store.ErrUnrecorded) {
		t.Errorf("a machine added without its record: %v", err)
	}
	if _, ok := e.st.Machine("edge"); ok {
		t.Error("the machine stayed")
	}
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "box", Address: box.Address, Replace: true}); !errors.Is(err, store.ErrUnrecorded) {
		t.Errorf("a machine registered again without its record: %v", err)
	}
	if m, _ := e.st.Machine("box"); m != box {
		t.Errorf("box changed: %+v", m)
	}
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat-new", Address: store.ConnectorAddress, ConnectorKey: authorized(signer(t))}); !errors.Is(err, store.ErrUnrecorded) {
		t.Errorf("a connector machine added without its record: %v", err)
	}
	if _, ok := e.st.Machine("nat-new"); ok {
		t.Error("the connector machine stayed")
	}
	if _, err := e.g.SetConnector(ctx, cli, "nat-rec", authorized(signer(t))); !errors.Is(err, store.ErrUnrecorded) {
		t.Errorf("a connector key set without its record: %v", err)
	}
	if m, _ := e.st.Machine("nat-rec"); m.ConnectorFingerprint != ssh.FingerprintSHA256(ck.PublicKey()) {
		t.Errorf("the connector key changed: %+v", m)
	}
	e.connectOut(t, "nat-rec", ck)
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	if _, err := e.g.Run(ctx, who, "nat-rec", RunInput{Command: "true"}); err == nil || !strings.Contains(err.Error(), "audit log cannot be written") {
		t.Errorf("a host key pinned without its record: %v", err)
	}
	if m, _ := e.st.Machine("nat-rec"); m.HostKey != "" || m.HostKeys != "" {
		t.Errorf("the host key was pinned: %+v", m)
	}
	if got := e.bot.all(); got != echoes {
		t.Errorf("echoed what was not made: %q", strings.TrimPrefix(got, echoes))
	}

	if _, err := db.Exec("DROP TRIGGER reject_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "edge", Address: "edge.example.net"}); err != nil || !audited(e.st, "machine_add", "edge", "edge.example.net:22") {
		t.Errorf("a machine added once its record can be written: %v", err)
	}
	nk := signer(t)
	if _, err := e.g.SetConnector(ctx, cli, "nat-rec", authorized(nk)); err != nil || !audited(e.st, "machine_connector", "nat-rec", ssh.FingerprintSHA256(nk.PublicKey())) {
		t.Errorf("a connector key set once its record can be written: %v", err)
	}
	e.connectOut(t, "nat-rec", nk)
	waitFor(t, "the host key", func() bool {
		m, _ := e.st.Machine("nat-rec")
		return m.HostKey != "" && audited(e.st, "machine_host_key", "nat-rec", fingerprint(m.HostKey))
	})
}
