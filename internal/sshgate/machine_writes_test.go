package sshgate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// people adds alice, an owner, and bob, an operator, and returns them as the console names them.
func (e *env) people(t *testing.T) (alice, bob Actor) {
	t.Helper()
	code, _, _ := e.st.CreateSetupLink()
	if _, err := e.st.Join(code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := e.st.CreateInvitation("operator", "", "alice")
	if _, err := e.st.Join(inv, "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	return Actor{Name: "alice", Via: "console", Key: "user:alice"}, Actor{Name: "bob", Via: "console", Key: "user:bob"}
}

// token makes a token with machines:ssh and machines:write and the machine access given.
func (e *env) token(t *testing.T, name string, access store.MachineAccess) Actor {
	t.Helper()
	if _, _, err := e.st.CreateToken(name, []string{"machines:ssh", "machines:write"}, time.Hour, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UpdateToken(name, func(tk *store.Token) error { tk.MachineAccess = access; return nil }); err != nil {
		t.Fatal(err)
	}
	return Actor{Name: name, Via: "api", Key: "token:" + name}
}

// TestMachineWritesNeedAllMachines: a credential that names the machines it reaches uses them and never
// changes a machine, the ones it names included; a credential with access to all machines changes those
// that are not critical.
func TestMachineWritesNeedAllMachines(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := e.st.PutMachine(store.Machine{Name: "db", Address: "db.example.com:22", User: "beacon", HostKey: "ssh-ed25519 AAAA"}); err != nil {
		t.Fatal(err)
	}
	boxed := e.token(t, "boxed", store.NamedAccess("box"))
	note, crit := "x", true
	edited, err := e.g.Update(ctx, Actor{Name: "cli:root", Via: "cli"}, "box", MachineUpdate{Note: &note})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.st.Machine("box")

	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "all machines") {
			t.Errorf("%s by a credential that names its machines: %v", what, err)
		}
	}
	_, err = e.g.Add(ctx, boxed, MachineInput{Name: "box", Address: e.tg.addr, Replace: true})
	refused("re-pointing the machine it reaches", err)
	_, err = e.g.Add(ctx, boxed, MachineInput{Name: "db", Address: e.tg.addr, Replace: true})
	refused("re-pointing another machine", err)
	_, err = e.g.Add(ctx, boxed, MachineInput{Name: "new", Address: e.tg.addr})
	refused("adding a machine", err)
	other := "y"
	_, err = e.g.Update(ctx, boxed, "box", MachineUpdate{Note: &other})
	refused("changing a machine", err)
	history, err := e.g.MachineHistory("box", 0, 10)
	if err != nil || len(history.Revisions) < 2 {
		t.Fatalf("history: %+v %v", history, err)
	}
	oldest := history.Revisions[len(history.Revisions)-1].Version
	_, err = e.g.RestoreMachine(ctx, boxed, "box", MachineRestore{Version: oldest, IfVersion: edited.Version})
	refused("restoring a machine's settings", err)
	refused("removing a machine", e.g.Remove(ctx, boxed, "box"))
	_, err = e.g.SetConnector(ctx, boxed, "box", authorized(signer(t)))
	refused("setting a connector", err)
	if after, _ := e.st.Machine("box"); after != before {
		t.Fatalf("a refused change changed the machine: %+v", after)
	}
	if _, ok := e.st.Machine("new"); ok {
		t.Fatal("a refused machine was added")
	}
	// it still uses the machine it names
	if _, err := e.g.machineFor(boxed, "box"); err != nil {
		t.Errorf("the credential no longer reaches its machine: %v", err)
	}

	// access to all machines changes those that are not critical
	wide := e.token(t, "wide", store.AllAccess())
	if _, err := e.g.Update(ctx, wide, "box", MachineUpdate{Note: &other}); err != nil {
		t.Fatalf("a note by a credential with all machines: %v", err)
	}
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "box", Address: e.tg.addr, Replace: true}); err != nil {
		t.Fatalf("re-pinning by a credential with all machines: %v", err)
	}
	// but marks no machine critical, and changes no critical machine
	_, err = e.g.Update(ctx, wide, "box", MachineUpdate{Critical: &crit})
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "owner") {
		t.Errorf("marking critical by a credential: %v", err)
	}
	if _, err := e.g.Update(ctx, Actor{Name: "cli:root", Via: "cli"}, "db", MachineUpdate{Critical: &crit}); err != nil {
		t.Fatal(err)
	}
	for what, err := range map[string]error{
		"re-pointing": func() error {
			_, err := e.g.Add(ctx, wide, MachineInput{Name: "db", Address: e.tg.addr, Replace: true, Critical: true})
			return err
		}(),
		"editing": func() error { _, err := e.g.Update(ctx, wide, "db", MachineUpdate{Note: &other}); return err }(),
		"unmarking": func() error {
			off := false
			_, err := e.g.Update(ctx, wide, "db", MachineUpdate{Critical: &off})
			return err
		}(),
		"removing": e.g.Remove(ctx, wide, "db"),
		"adding marked": func() error {
			_, err := e.g.Add(ctx, wide, MachineInput{Name: "vault", Address: e.tg.addr, Critical: true})
			return err
		}(),
	} {
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("%s a critical machine by a credential with all machines: %v", what, err)
		}
	}
	if m, _ := e.st.Machine("db"); !m.Critical || m.Address != "db.example.com:22" {
		t.Fatalf("the critical machine changed: %+v", m)
	}

	// an owner does
	alice, bob := e.people(t)
	off := false
	if _, err := e.g.Update(ctx, bob, "db", MachineUpdate{Critical: &off}); !errors.Is(err, ErrForbidden) {
		t.Errorf("an operator unmarks a critical machine: %v", err)
	}
	if _, err := e.g.Update(ctx, alice, "db", MachineUpdate{Note: &note}); err != nil {
		t.Errorf("an owner edits a critical machine: %v", err)
	}
	if err := e.g.Remove(ctx, alice, "db"); err != nil {
		t.Errorf("an owner removes a critical machine: %v", err)
	}
}

// TestMachineAccountChanges: the account Beacon signs certificates for is changed by a person or beacon
// on the host, never by another credential, and root only by beacon on the host.
func TestMachineAccountChanges(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	wide := e.token(t, "wide", store.AllAccess())
	_, bob := e.people(t)
	root, ops := "root", "ops"
	if _, err := e.g.Update(ctx, wide, "box", MachineUpdate{User: &ops}); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "person") {
		t.Errorf("a credential changes the account: %v", err)
	}
	if _, err := e.g.Add(ctx, wide, MachineInput{Name: "box", Address: e.tg.addr, User: "ops", Replace: true}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a credential changes the account by registering again: %v", err)
	}
	for _, who := range []Actor{wide, bob} {
		if _, err := e.g.Update(ctx, who, "box", MachineUpdate{User: &root}); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "root") {
			t.Errorf("%s makes the account root: %v", who.Name, err)
		}
		if _, err := e.g.Add(ctx, who, MachineInput{Name: "fresh", Address: e.tg.addr, User: "root"}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s adds a machine reached as root: %v", who.Name, err)
		}
	}
	if m, _ := e.st.Machine("box"); m.User != "beacon" {
		t.Fatalf("the account changed: %s", m.User)
	}
	if _, err := e.g.Update(ctx, bob, "box", MachineUpdate{User: &ops}); err != nil {
		t.Errorf("a person changes the account: %v", err)
	}
	if _, err := e.g.Update(ctx, Actor{Name: "cli:root", Via: "cli"}, "box", MachineUpdate{User: &root}); err != nil {
		t.Errorf("beacon on the host makes the account root: %v", err)
	}
}

// TestConnectorKeyIsOneMachines: no two machines connect with the same key.
func TestConnectorKeyIsOneMachines(t *testing.T) {
	e := setup(t)
	key := signer(t)
	e.addConnector(t, "nat-1", key, "")
	cli := Actor{Name: "cli:root", Via: "cli"}
	if _, err := e.g.Add(context.Background(), cli, MachineInput{Name: "nat-2", Address: store.ConnectorAddress, ConnectorKey: authorized(key)}); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "nat-1") {
		t.Errorf("a second machine with the first's connector key: %v", err)
	}
	if err := e.st.PutMachine(store.Machine{Name: "nat-3", Address: store.ConnectorAddress, User: "beacon", ConnectorKey: authorized(key)}); err != nil {
		t.Fatal(err) // no fingerprint: nothing to compare
	}
	m, _ := e.st.Machine("nat-1")
	m.Name = "nat-4"
	if err := e.st.PutMachine(m); !errors.Is(err, store.ErrExists) {
		t.Errorf("the store keeps two machines with one connector key: %v", err)
	}
}

// TestConnectorBoundToKeyAndRegistration: a connector's connection carries its machine only while the
// machine has the key it signed in with, in the registration it signed in to.
func TestConnectorBoundToKeyAndRegistration(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	cli := Actor{Name: "cli:root", Via: "cli"}
	who := Actor{Name: "claude-code", Via: "mcp", Key: "token:claude-code"}
	ck := signer(t)
	e.addConnector(t, "nat", ck, authorized(e.tg.hostKey))
	cl := e.connectOut(t, "nat", ck)
	waitFor(t, "the connector", func() bool { return e.g.connectors.get("nat") != nil })
	if _, err := e.g.Run(ctx, who, "nat", RunInput{Command: "true"}); err != nil {
		t.Fatalf("through the connector: %v", err)
	}

	// registered again: the connection signed in to the old registration ends
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat", Address: store.ConnectorAddress, Replace: true, HostKey: authorized(e.tg.hostKey)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the old connection to close", func() bool { return closed(cl) })

	// a connection whose machine changed under it, without Beacon closing it, carries nothing
	cl = e.connectOut(t, "nat", ck)
	waitFor(t, "the connector", func() bool { return e.g.connectors.get("nat") != nil })
	m, _ := e.st.Machine("nat")
	again := m
	again.Added = m.Added.Add(time.Second)
	if _, err := e.st.RegisterMachine(again, &m, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.Run(ctx, who, "nat", RunInput{Command: "true"}); !errors.Is(err, ErrDisconnected) {
		t.Errorf("a connection from an earlier registration: %v", err)
	}
	waitFor(t, "the stale connection to close", func() bool { return closed(cl) })

	// made a dialled machine: its connector ends
	cl = e.connectOut(t, "nat", ck)
	waitFor(t, "the connector", func() bool { return e.g.connectors.get("nat") != nil })
	if _, err := e.g.Add(ctx, cli, MachineInput{Name: "nat", Address: e.tg.addr, Replace: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the connector of a dialled machine to close", func() bool { return closed(cl) })
}
