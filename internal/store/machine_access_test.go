package store

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// machines enrols box and ca, ca critical.
func machines(t *testing.T, s *Store) {
	t.Helper()
	for _, m := range []Machine{{Name: "box", Address: "box.example.com:22", User: "beacon"}, {Name: "ca", Address: "ca.example.com:22", User: "beacon", Critical: true}} {
		if err := s.PutMachine(m); err != nil {
			t.Fatal(err)
		}
	}
}

// TestEmptyMachineAccessReachesNoMachine: nothing but an explicit all reaches every machine. An empty
// list, a missing access and a stored record from before access was explicit reach none.
func TestEmptyMachineAccessReachesNoMachine(t *testing.T) {
	s, _ := openTest(t)
	owner(t, s, "alice")
	machines(t, s)

	secret, tok, err := s.CreateToken("ci", []string{"status:read", "machines:ssh"}, time.Hour, "test")
	if err != nil || tok.Access != AccessNamed || len(tok.Machines) != 0 {
		t.Fatalf("a new token: %+v %v", tok, err)
	}
	p, _ := s.Authenticate(secret)
	if p.Reaches("box") || p.Reaches("later") || p.Has("machines:ssh") || !p.NoMachines() {
		t.Errorf("a token given no machines: %+v", p)
	}
	if _, err := s.UpdateToken("ci", func(t *Token) error { t.MachineAccess = NamedAccess(); return nil }); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Authenticate(secret); p.Reaches("box") {
		t.Error("an empty named list reaches a machine")
	}
	if _, err := s.UpdateToken("ci", func(t *Token) error { t.MachineAccess = MachineAccess{Access: "everything"}; return nil }); err == nil {
		t.Error("an unknown machine access was stored")
	}
	if _, err := s.UpdateToken("ci", func(t *Token) error { t.MachineAccess = MachineAccess{Access: AccessEvery}; return nil }); err == nil {
		t.Error("a token was given every machine, critical ones included")
	}

	a, err := s.CreateAgent(Agent{Name: "bot", Owner: "alice", Preset: "operator"})
	if err != nil || a.Access != AccessNamed || !a.None() {
		t.Fatalf("an agent given no machines: %+v %v", a, err)
	}
	if _, err := s.CreateAgent(Agent{Name: "bad", Owner: "alice", Preset: "operator", MachineAccess: MachineAccess{Access: "any"}}); err == nil {
		t.Error("an agent with an unknown machine access")
	}

	// a record stored before access was explicit, not yet moved: it reaches nothing
	legacy := Token{Name: "old", Hash: hashToken("lbt_old"), Scopes: []string{"machines:ssh"}, Expires: time.Now().Add(time.Hour)}
	raw, _ := json.Marshal(legacy)
	if strings.Contains(string(raw), "machine_access") {
		t.Fatalf("the fixture is not from before: %s", raw)
	}
	if err := s.db.Update(func(tx kv.Tx) error { return tx.Put(bucketTokens, []byte("old"), raw) }); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTokens(); err != nil {
		t.Fatal(err)
	}
	for _, x := range s.Tokens() {
		if x.Name != "old" {
			continue
		}
		if p, _ := s.TokenPrincipal(x); p.Reaches("box") || p.Has("machines:ssh") {
			t.Errorf("a record without machine access reaches a machine: %+v", p)
		}
	}
}

// TestCriticalMachinesTakeANamedGrant: all covers every machine that is not critical, machines added
// later included; a critical machine is reached only by a credential that names it, or by an owner.
func TestCriticalMachinesTakeANamedGrant(t *testing.T) {
	s, _ := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "bob", "operator")
	machines(t, s)

	secret, _, _ := s.CreateToken("wide", []string{"machines:ssh"}, time.Hour, "test")
	_, _ = s.UpdateToken("wide", func(t *Token) error { t.MachineAccess = AllAccess(); return nil })
	p, _ := s.Authenticate(secret)
	if !p.Reaches("box") || !p.Reaches("added-later") || p.Reaches("ca") || !p.IsAll() || !p.MachinesLimited() {
		t.Errorf("all: %+v", p)
	}
	_, _ = s.UpdateToken("wide", func(t *Token) error { t.MachineAccess = AllAccess("ca"); return nil })
	if p, _ := s.Authenticate(secret); !p.Reaches("ca") || !p.Reaches("box") || p.MachinesLimited() {
		t.Errorf("all, naming the critical machine: %+v", p)
	}
	_, _ = s.UpdateToken("wide", func(t *Token) error { t.MachineAccess = NamedAccess("ca"); return nil })
	if p, _ := s.Authenticate(secret); !p.Reaches("ca") || p.Reaches("box") {
		t.Errorf("a named list with the critical machine: %+v", p)
	}

	// marking a machine critical takes it away from all at once
	_, _ = s.UpdateToken("wide", func(t *Token) error { t.MachineAccess = AllAccess(); return nil })
	m, _ := s.Machine("box")
	m.Critical = true
	_ = s.PutMachine(m)
	if p, _ := s.Authenticate(secret); p.Reaches("box") {
		t.Error("a machine marked critical is still reached through all")
	}
	m.Critical = false
	_ = s.PutMachine(m)

	// people: an owner reaches every machine; anyone else a critical one only when it is named on them
	alice, _ := s.AccountPrincipal("alice")
	if !alice.Reaches("ca") || !alice.Owner() || !alice.IsEvery() {
		t.Errorf("an owner: %+v", alice)
	}
	bob, _ := s.AccountPrincipal("bob")
	if !bob.Reaches("box") || bob.Reaches("ca") || bob.Owner() {
		t.Errorf("an operator: %+v", bob)
	}
	// and an agent no more than its owner, whatever it names
	if _, err := s.CreateAgent(Agent{Name: "bobs", Owner: "bob", Preset: "operator", MachineAccess: AllAccess("ca")}); err != nil {
		t.Fatal(err)
	}
	agentSecret, _, _ := s.CreateAgentToken("bobs", time.Hour, "test")
	if p, _ := s.Authenticate(agentSecret); !p.Reaches("box") || p.Reaches("ca") {
		t.Errorf("an agent beyond its owner: %+v", p)
	}
	if _, _, err := s.SetCriticalMachines("bob", NamedAccess("ca")); err != nil {
		t.Fatal(err)
	}
	if bob, _ := s.AccountPrincipal("bob"); !bob.Reaches("ca") {
		t.Errorf("an operator named on the critical machine: %+v", bob)
	}
	if p, _ := s.Authenticate(agentSecret); !p.Reaches("ca") {
		t.Errorf("an agent within its owner's named machine: %+v", p)
	}
	// an agent of an owner still needs the critical machine named
	if _, err := s.CreateAgent(Agent{Name: "alices", Owner: "alice", Preset: "operator", MachineAccess: AllAccess()}); err != nil {
		t.Fatal(err)
	}
	aliceSecret, _, _ := s.CreateAgentToken("alices", time.Hour, "test")
	if p, _ := s.Authenticate(aliceSecret); !p.Reaches("box") || p.Reaches("ca") {
		t.Errorf("an owner's agent reaches a critical machine unnamed: %+v", p)
	}
	// a grant within its agent
	g, _ := s.CreateGrant(Grant{Agent: "alices", Client: "c", Preset: "operator", MachineAccess: AllAccess("ca"), Expires: time.Now().Add(time.Hour)}, false)
	if p, _ := s.Authenticate(g.Access); !p.Reaches("box") || p.Reaches("ca") {
		t.Errorf("a grant beyond its agent: %+v", p)
	}
}

// TestNamedMachineEnds: a named machine's access ends at its time, and the tidy forgets it, in the
// audit log.
func TestNamedMachineEnds(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "bob", "operator")
	machines(t, s)
	end := now.Add(2 * time.Hour)
	secret, _, _ := s.CreateToken("deploy", []string{"machines:ssh"}, 24*time.Hour, "test")
	_, _ = s.UpdateToken("deploy", func(t *Token) error {
		t.MachineAccess = AllAccess("ca")
		t.MachineUntil = map[string]time.Time{"ca": end}
		return nil
	})
	if _, err := s.CreateAgent(Agent{Name: "bot", Owner: "alice", Preset: "operator", MachineAccess: MachineAccess{Access: AccessNamed, Machines: []string{"ca"}, MachineUntil: map[string]time.Time{"ca": end}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetCriticalMachines("bob", MachineAccess{Access: AccessNamed, Machines: []string{"ca"}, MachineUntil: map[string]time.Time{"ca": end}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Authenticate(secret); !p.Reaches("ca") || !p.Until("ca").Equal(end) {
		t.Fatalf("before the end: %+v", p)
	}
	if ended, err := s.TidyMachineAccess(*now); err != nil || len(ended) != 0 {
		t.Fatalf("a tidy before the end: %+v %v", ended, err)
	}

	*now = end // at the end, the access stops, whether or not the tidy ran
	if p, _ := s.Authenticate(secret); p.Reaches("ca") || !p.Reaches("box") {
		t.Errorf("at the end: %+v", p)
	}
	if bob, _ := s.AccountPrincipal("bob"); bob.Reaches("ca") {
		t.Error("a person's named machine past its end")
	}
	ended, err := s.TidyMachineAccess(*now)
	if err != nil || len(ended) != 3 {
		t.Fatalf("the tidy: %+v %v", ended, err)
	}
	if tok := tokenNamed(s, "deploy"); tok.Names("ca") || tok.Access != AccessAll {
		t.Errorf("the tidied token: %+v", tok.MachineAccess)
	}
	if a, _ := s.Agent("bot"); a.Names("ca") || a.Access != AccessNamed {
		t.Errorf("the tidied agent: %+v", a.MachineAccess)
	}
	if a, _ := s.Account("bob"); len(a.CriticalMachines) != 0 {
		t.Errorf("the tidied account: %+v", a.CriticalMachines)
	}
	audited := 0
	for _, e := range s.AuditLog(100) {
		if e.Action == "machine_access_end" && strings.Contains(e.Detail, "ca") {
			audited++
		}
	}
	if audited != 3 {
		t.Errorf("ended accesses audited: %d", audited)
	}
	if again, _ := s.TidyMachineAccess(*now); len(again) != 0 {
		t.Errorf("a second tidy: %+v", again)
	}
}

func tokenNamed(s *Store, name string) Token {
	for _, t := range s.Tokens() {
		if t.Name == name {
			return t
		}
	}
	return Token{}
}

// TestMigrateMachineAccess: credentials from before access was explicit keep reaching what they did,
// once, with each change audited; nothing written afterwards is ever moved.
func TestMigrateMachineAccess(t *testing.T) {
	s, _ := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "bob", "oncall")
	machines(t, s)
	put := func(bucket []byte, key string, v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		delete(m, "machine_access") // as stored before
		raw, _ = json.Marshal(m)
		if err := s.db.Update(func(tx kv.Tx) error { return tx.Put(bucket, []byte(key), raw) }); err != nil {
			t.Fatal(err)
		}
	}
	exp := time.Now().Add(time.Hour)
	put(bucketTokens, "everywhere", Token{Name: "everywhere", Hash: hashToken("lbt_a"), Scopes: []string{"machines:ssh", "machines:write"}, Expires: exp})
	put(bucketTokens, "one", Token{Name: "one", Hash: hashToken("lbt_b"), Scopes: []string{"machines:ssh"}, MachineAccess: MachineAccess{Machines: []string{"box"}}, Expires: exp})
	put(bucketTokens, "probe-x", Token{Name: "probe-x", Hash: hashToken("lbt_c"), Scopes: []string{"probe:write"}, Subject: "x", Expires: exp})
	put(bucketAgents, "agent", Agent{Name: "agent", Owner: "alice", Preset: "operator"})
	put(bucketAgents, "limited", Agent{Name: "limited", Owner: "bob", Preset: "oncall", MachineAccess: MachineAccess{Machines: []string{"box", "ca"}}})
	put(bucketGrants, "g1", Grant{ID: "g1", Agent: "agent", Client: "c", Preset: "operator", Expires: exp})
	if err := s.RefreshTokens(); err != nil {
		t.Fatal(err)
	}

	moved, err := s.MigrateMachineAccess()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range moved {
		got[c.Kind+":"+c.Name] = c.Access.String()
	}
	want := map[string]string{"token:everywhere": "all + ca", "token:one": "named box", "agent:agent": "all + ca",
		"agent:limited": "named box, ca", "grant:g1": "all + ca", "account:bob": "named ca"}
	if len(got) != len(want) {
		t.Errorf("moved %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s moved to %q, want %q", k, got[k], v)
		}
	}
	if p := tokenNamed(s, "probe-x"); p.Access != AccessNamed || len(p.Machines) != 0 {
		t.Errorf("a machine's token: %+v", p.MachineAccess)
	}
	everywhere, _ := s.TokenPrincipal(tokenNamed(s, "everywhere"))
	if !everywhere.Reaches("ca") || !everywhere.Reaches("box") || !everywhere.IsAll() {
		t.Errorf("an empty list keeps reaching every machine: %+v", everywhere)
	}
	if a, _ := s.Account("alice"); len(a.CriticalMachines) != 0 {
		t.Errorf("an owner named on machines: %+v", a.CriticalMachines)
	}
	audited := 0
	for _, e := range s.AuditLog(100) {
		if e.Action == "machine_access_migrate" {
			audited++
		}
	}
	if audited != len(want) {
		t.Errorf("audited %d migrations, want %d", audited, len(want))
	}

	// once only: what is stored afterwards without an access stays without one
	put(bucketTokens, "later", Token{Name: "later", Hash: hashToken("lbt_d"), Scopes: []string{"machines:ssh"}, Expires: exp})
	if again, err := s.MigrateMachineAccess(); err != nil || len(again) != 0 {
		t.Fatalf("a second migration: %+v %v", again, err)
	}
	_ = s.RefreshTokens()
	if later, _ := s.TokenPrincipal(tokenNamed(s, "later")); later.Reaches("box") {
		t.Error("a record stored after the migration was moved to all")
	}
}

func TestParseMachineList(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in, want string
		forever  []string
		bad      bool
	}{
		{in: "", want: "none"},
		{in: "none", want: "none"},
		{in: "all", want: "all"},
		{in: "box,ca", want: "named box, ca"},
		{in: "all,ca@72h", want: "all + ca (until 2026-11-05 10:00Z)"},
		{in: "ca@30d", want: "named ca (until 2026-12-02 10:00Z)"},
		{in: "ca@2026-12-31", want: "named ca (until 2026-12-31 00:00Z)"},
		{in: "ca@never", want: "named ca", forever: []string{"ca"}},
		{in: "ca@10m", bad: true},
		{in: "ca@2020-01-01", bad: true},
		{in: "ca@soon", bad: true},
		{in: "Bad_Name", bad: true},
	} {
		a, forever, err := ParseMachineList(tc.in, now)
		switch {
		case tc.bad && err == nil:
			t.Errorf("%q: no error", tc.in)
		case !tc.bad && (err != nil || a.String() != tc.want || !slices.Equal(forever, tc.forever)):
			t.Errorf("%q: %q %v %v, want %q %v", tc.in, a.String(), forever, err, tc.want, tc.forever)
		}
	}
}

func TestMachineAccessWithinAndIntersect(t *testing.T) {
	critical := func(m string) bool { return m == "ca" }
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	ending := MachineAccess{Access: AccessAll, Machines: []string{"ca"}, MachineUntil: map[string]time.Time{"ca": end}}
	for _, tc := range []struct {
		name string
		a, b MachineAccess
		want bool
	}{
		{"named within all", NamedAccess("box"), AllAccess(), true},
		{"a critical machine is not within all", NamedAccess("ca"), AllAccess(), false},
		{"all is not within named", AllAccess(), NamedAccess("box"), false},
		{"all within all", AllAccess(), AllAccess(), true},
		{"a critical name within its name", AllAccess("ca"), AllAccess("ca"), true},
		{"no end is not within an end", AllAccess("ca"), ending, false},
		{"an end within no end", ending, AllAccess("ca"), true},
		{"anything within every machine", AllAccess("ca"), MachineAccess{Access: AccessEvery}, true},
		{"none within named none", NamedAccess(), NamedAccess(), true},
	} {
		if got := tc.a.Within(tc.b, critical); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	for _, tc := range []struct {
		name   string
		layers []MachineAccess
		want   string
	}{
		{"an agent within an owner", []MachineAccess{AllAccess("ca"), {Access: AccessEvery}}, "all + ca"},
		{"an agent within a person", []MachineAccess{AllAccess("ca"), AllAccess()}, "all"},
		{"named within all", []MachineAccess{NamedAccess("box", "ca"), AllAccess()}, "named box"},
		{"the earliest end", []MachineAccess{ending, AllAccess("ca")}, "all + ca (until 2026-12-01 00:00Z)"},
		{"nothing in common", []MachineAccess{NamedAccess("box"), NamedAccess("db")}, "none"},
		{"a missing access", []MachineAccess{{}, AllAccess()}, "none"},
	} {
		if got := IntersectAccess(critical, tc.layers...).String(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
