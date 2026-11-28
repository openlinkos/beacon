package store

import (
	"strings"
	"testing"
	"time"
)

// TestCriticalMarkEndsNames: a machine marked critical, or registered critical, keeps the tokens, agents
// and OAuth grants that name it until CriticalGrantTTL from then at the latest, each audited; a shorter
// end stays, credentials with all machines lose it, and people's accounts are not changed.
func TestCriticalMarkEndsNames(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "bob", "operator")
	for _, name := range []string{"box", "db"} {
		if _, err := s.RegisterMachine(Machine{Name: name, Address: name + ".example.com:22", User: "beacon"}, nil, "test"); err != nil {
			t.Fatal(err)
		}
	}
	short := now.Add(24 * time.Hour)
	tokens := map[string]MachineAccess{
		"named": NamedAccess("db"),
		"short": {Access: AccessNamed, Machines: []string{"db"}, MachineUntil: map[string]time.Time{"db": short}},
		"wide":  AllAccess(),
		"other": NamedAccess("box", "vault"),
	}
	secrets := map[string]string{}
	for name, access := range tokens {
		secret, _, err := s.CreateToken(name, []string{"machines:ssh"}, 90*24*time.Hour, "test")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateToken(name, func(tk *Token) error { tk.MachineAccess = access; return nil }); err != nil {
			t.Fatal(err)
		}
		secrets[name] = secret
	}
	if _, err := s.CreateAgent(Agent{Name: "bot", Owner: "alice", Preset: "operator", MachineAccess: NamedAccess("box", "db")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGrant(Grant{Agent: "bot", Client: "c", ClientName: "c", Preset: "operator", MachineAccess: NamedAccess("db"),
		Expires: now.Add(90 * 24 * time.Hour)}, false); err != nil {
		t.Fatal(err)
	}
	reaches := func(name, machine string) bool {
		p, _ := s.Authenticate(secrets[name])
		return p.Reaches(machine)
	}

	on := true
	if _, _, err := s.PatchMachineMetadata("db", MachineMetadataUpdate{Critical: &on}, 0, "alice", "console", nil); err != nil {
		t.Fatal(err)
	}
	until := now.Add(CriticalGrantTTL)
	if tk := tokenNamed(s, "named"); !tk.Until("db").Equal(until) {
		t.Errorf("a token's name of db: until %v, want %v", tk.Until("db"), until)
	}
	if tk := tokenNamed(s, "short"); !tk.Until("db").Equal(short) {
		t.Errorf("a shorter end: until %v, want %v", tk.Until("db"), short)
	}
	if a, _ := s.Agent("bot"); !a.Until("db").Equal(until) || !a.Until("box").IsZero() {
		t.Errorf("the agent: %+v", a.MachineAccess)
	}
	for _, g := range s.Grants("bot") {
		if !g.Until("db").Equal(until) {
			t.Errorf("the grant: %+v", g.MachineAccess)
		}
	}
	if a, _ := s.Account("bob"); len(a.CriticalMachines) != 0 || len(a.CriticalUntil) != 0 {
		t.Errorf("a person's account changed: %+v", a)
	}
	audited := 0
	for _, e := range s.AuditLog(100) {
		if e.Action == "machine_access_critical" {
			audited++
			if e.Actor != "alice" || e.Via != "console" || !strings.Contains(e.Detail, "machine db marked critical") {
				t.Errorf("audit %+v", e)
			}
		}
	}
	if audited != 3 { // the token named, the agent and the grant
		t.Errorf("%d changes audited, want 3", audited)
	}
	if !reaches("named", "db") || reaches("wide", "db") || !reaches("wide", "box") {
		t.Error("after the mark: the named token keeps db, the one with all machines does not")
	}
	// marking it again changes nothing
	if _, _, err := s.PatchMachineMetadata("db", MachineMetadataUpdate{Note: new(string)}, 0, "alice", "console", nil); err != nil {
		t.Fatal(err)
	}

	// a machine registered critical: the names of it given before it existed take an end too
	if _, err := s.RegisterMachine(Machine{Name: "vault", Address: "vault.example.com:22", User: "beacon", Critical: true, AddedBy: "alice"}, nil, "cli"); err != nil {
		t.Fatal(err)
	}
	if tk := tokenNamed(s, "other"); !tk.Until("vault").Equal(until) || !tk.Until("box").IsZero() {
		t.Errorf("a name of a machine registered critical: %+v", tk.MachineAccess)
	}

	*now = until
	if reaches("named", "db") || reaches("other", "vault") || !reaches("other", "box") {
		t.Error("the names given before the mark outlived their end")
	}
}
