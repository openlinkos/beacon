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
