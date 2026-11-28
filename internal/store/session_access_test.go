package store

import (
	"testing"
	"time"
)

func TestMachineScopedSessionAccess(t *testing.T) {
	s, now := openTest(t)
	old, err := s.RegisterMachine(Machine{Name: "edge-1", Added: now.Add(-time.Hour)}, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.RegisterMachine(Machine{Name: old.Name, Added: *now}, &old, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterMachine(Machine{Name: "undated"}, nil, "test"); err != nil {
		t.Fatal(err)
	}
	scoped := s.SessionAccess(Principal{Scopes: []string{"sessions:read"}, Machines: []string{current.Name, "undated", "retired"}})
	metadata := s.SessionAccess(Principal{Scopes: []string{"status:read"}, Machines: []string{current.Name}})
	person := s.SessionAccess(Principal{User: "alice", Scopes: []string{"sessions:read"}, MachineAccess: MachineAccess{Access: AccessEvery}})
	if !scoped.Details() || metadata.Details() || !person.Details() {
		t.Fatal("session detail permission was coupled to the machine filter")
	}
	for _, tc := range []struct {
		name string
		r    SessionRecord
		want bool
	}{
		{"current registration", SessionRecord{Machine: current.Name, MachineRegistration: current.Registration()}, true},
		{"prior registration with a recent timestamp", SessionRecord{Machine: current.Name, MachineRegistration: old.Registration(), Started: now.Add(time.Minute)}, false},
		{"legacy at enrolment", SessionRecord{Machine: current.Name, Started: *now}, true},
		{"legacy before enrolment", SessionRecord{Machine: current.Name, Started: now.Add(-time.Nanosecond)}, false},
		{"legacy without a start", SessionRecord{Machine: current.Name}, false},
		{"legacy without enrolment time", SessionRecord{Machine: "undated", Started: *now}, false},
		{"missing selected machine", SessionRecord{Machine: "retired", Started: *now}, false},
		{"other machine", SessionRecord{Machine: "edge-2", Started: *now}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if scoped.Visible(tc.r) != tc.want || metadata.Visible(tc.r) != tc.want {
				t.Fatalf("machine check with and without detail scope: got %v and %v, want %v",
					scoped.Visible(tc.r), metadata.Visible(tc.r), tc.want)
			}
			if !person.Visible(tc.r) {
				t.Fatal("machine retention rules constrained the unscoped person")
			}
		})
	}
	if s.SessionAccess(Principal{}).Visible(SessionRecord{Machine: current.Name}) {
		t.Fatal("a principal without read permissions could see session metadata")
	}
}

func TestDisjointOAuthSessionAccess(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	if _, err := s.CreateAgent(Agent{Name: "fixture", Owner: "alice", Preset: "oncall", Machines: []string{"edge-2"}}); err != nil {
		t.Fatal(err)
	}
	grant, err := s.CreateGrant(Grant{Agent: "fixture", Client: "fixture-client", Preset: "readonly",
		Machines: []string{"edge-1"}, Expires: now.Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s.Authenticate(grant.Access)
	if !ok || !p.Has("status:read") || !p.Has("sessions:read") || p.Has("machines:ssh") || !p.NoMachines() {
		t.Fatalf("disjoint grant was not represented independently of its read scopes: %+v", p)
	}
	resolved, ok := s.PrincipalWith(p.Key(), p.Cred())
	if !ok {
		t.Fatal("the same grant did not resolve through its credential")
	}
	for _, q := range []Principal{p, resolved} {
		a := s.SessionAccess(q)
		if !q.NoMachines() || !a.Details() || a.Visible(SessionRecord{Machine: "edge-1"}) ||
			a.Visible(SessionRecord{Machine: "edge-2"}) {
			t.Fatal("disjoint machine scope was treated as unlimited")
		}
	}

	if _, err := s.UpdateAgent("fixture", func(a *Agent) error { a.MachineAccess = NamedAccess("edge-1"); return nil }); err != nil {
		t.Fatal(err)
	}
	m, err := s.RegisterMachine(Machine{Name: "edge-1", Added: *now}, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	p, ok = s.Authenticate(grant.Access)
	if !ok || p.NoMachines() || !s.SessionAccess(p).Visible(SessionRecord{Machine: m.Name, MachineRegistration: m.Registration()}) {
		t.Fatal("the live credential did not reflect the newly overlapping machine scope")
	}
}
