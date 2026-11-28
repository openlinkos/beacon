package store

import (
	"errors"
	"slices"
	"testing"
)

func TestNormalAddress(t *testing.T) {
	for in, want := range map[string]string{
		"Box.Example.com.:22":  "box.example.com:22",
		"10.0.0.5:022":         "10.0.0.5:22",
		"[::ffff:10.0.0.5]:22": "10.0.0.5:22",
		"[2001:DB8::1]:2222":   "[2001:db8::1]:2222",
		" 192.0.2.1:22 ":       "192.0.2.1:22",
		ConnectorAddress:       ConnectorAddress,
	} {
		if got := NormalAddress(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestMachineAliasRefused: the store registers no machine at another's address or with its host key,
// and sets no host key another machine has; machines recorded twice before are listed.
func TestMachineAliasRefused(t *testing.T) {
	s, _ := openTest(t)
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGdW4IRo2jQ8l5gDLrPqSYl7Dl1VcA3KLb5aR7Fo3uqo"
	if _, err := s.RegisterMachine(Machine{Name: "db", Address: "db.example.com:22", User: "beacon", HostKey: key + " root@db"}, nil, "test"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Machine{
		{Name: "db-2", Address: "DB.example.com.:22", User: "beacon", HostKey: "ssh-ed25519 AAAAother"},
		{Name: "db-3", Address: "192.0.2.7:22", User: "root", HostKey: key},
		{Name: "nat", Address: ConnectorAddress, User: "beacon", HostKey: key},
	} {
		if _, err := s.RegisterMachine(m, nil, "test"); !errors.Is(err, ErrMachineAlias) {
			t.Errorf("%s registered as db's second record: %v", m.Name, err)
		}
	}
	// connector machines share their address, not their host keys
	for _, name := range []string{"nat-1", "nat-2"} {
		if _, err := s.RegisterMachine(Machine{Name: name, Address: ConnectorAddress, User: "beacon"}, nil, "test"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := s.UpdateMachineIdentity("nat-1", func(Machine) bool { return true }, func(m *Machine) { m.HostKey = key }); !errors.Is(err, ErrMachineAlias) {
		t.Errorf("a connector machine learned db's host key: %v", err)
	}
	if m, _ := s.Machine("nat-1"); m.HostKey != "" {
		t.Errorf("the refused host key was stored: %+v", m)
	}
	// db registered again where it is, and edited, is not its own alias
	db, _ := s.Machine("db")
	again := db
	again.Added = db.Added.Add(1)
	if _, err := s.RegisterMachine(again, &db, "test"); err != nil {
		t.Errorf("db registered again in place: %v", err)
	}

	// what was recorded twice before is listed
	if err := s.PutMachine(Machine{Name: "old-alias", Address: "db.example.com:22", User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	if got := s.MachineAliases(); !slices.Equal(got, []string{"db and old-alias: machine old-alias is dialled at db.example.com:22"}) {
		t.Errorf("aliases: %q", got)
	}
}

// TestMachineAliasComparesEveryHostKey: a machine is known by every host key it offered, so no record
// shares any of them with another, and the machines recorded twice before are listed by any of them.
func TestMachineAliasComparesEveryHostKey(t *testing.T) {
	s, _ := openTest(t)
	ed := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGdW4IRo2jQ8l5gDLrPqSYl7Dl1VcA3KLb5aR7Fo3uqo"
	ec := "ecdsa-sha2-nistp256 AAAAvault"
	other := "ecdsa-sha2-nistp256 AAAAweb"
	if _, err := s.RegisterMachine(Machine{Name: "vault", Address: "vault.example.com:22", User: "beacon", HostKey: ec}, nil, "test"); err != nil {
		t.Fatal(err)
	}
	// a machine that offers vault's ECDSA key beside the Ed25519 key it pins is vault
	alias := Machine{Name: "alias", Address: "192.0.2.9:22", User: "beacon", HostKey: ed, HostKeys: ed + "\n" + ec}
	if _, err := s.RegisterMachine(alias, nil, "test"); !errors.Is(err, ErrMachineAlias) {
		t.Errorf("a machine offering vault's key: %v", err)
	}
	// a machine recorded with all its keys is known by each
	if _, err := s.RegisterMachine(Machine{Name: "web", Address: "web.example.com:22", User: "beacon", HostKey: ed, HostKeys: ed + "\n" + other}, nil, "test"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Machine("web"); !slices.Equal(m.KnownHostKeys(), []string{ed, other}) {
		t.Errorf("web is known by %q", m.KnownHostKeys())
	}
	if _, err := s.RegisterMachine(Machine{Name: "nat", Address: ConnectorAddress, User: "beacon", HostKey: other + " root@web"}, nil, "test"); !errors.Is(err, ErrMachineAlias) {
		t.Errorf("a machine pinned with web's other key: %v", err)
	}
	// keys recorded later are compared too
	if _, err := s.RegisterMachine(Machine{Name: "nat", Address: ConnectorAddress, User: "beacon", HostKey: "ssh-ed25519 AAAAnat"}, nil, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMachineIdentity("nat", func(Machine) bool { return true }, func(m *Machine) { m.HostKeys = m.HostKey + "\n" + ec }); !errors.Is(err, ErrMachineAlias) {
		t.Errorf("a machine whose other keys show it is vault: %v", err)
	}
	if m, _ := s.Machine("nat"); m.HostKeys != "" {
		t.Errorf("the refused keys were stored: %+v", m)
	}
	// registering vault again in place, now with all its keys, works; with web's among them, it does not
	v, _ := s.Machine("vault")
	again := v
	again.HostKeys = ec + "\nssh-ed25519 AAAAvault"
	if _, err := s.RegisterMachine(again, &v, "test"); err != nil {
		t.Errorf("vault registered again in place: %v", err)
	}
	v, _ = s.Machine("vault")
	again = v
	again.HostKeys = ec + "\n" + ed
	if _, err := s.RegisterMachine(again, &v, "test"); !errors.Is(err, ErrMachineAlias) {
		t.Errorf("vault registered again with web's key: %v", err)
	}

	// what was recorded twice before is listed by any key either is known by
	if err := s.PutMachine(Machine{Name: "web-old", Address: "192.0.2.80:22", User: "beacon", HostKey: other}); err != nil {
		t.Fatal(err)
	}
	if got := s.MachineAliases(); !slices.Equal(got, []string{"web and web-old: machine web-old has the host key " + other}) {
		t.Errorf("aliases: %q", got)
	}
}
