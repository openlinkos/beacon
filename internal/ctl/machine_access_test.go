package ctl

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestMachineListsOverTheSocket: the command line gives tokens, agents and people explicit machine
// access, with ends for critical machines; a token made without machines reaches none.
func TestMachineListsOverTheSocket(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	people := PeopleHandler(e.Bridge)
	for _, p := range []string{"/v1/users", "/v1/users/", "/v1/agents", "/v1/agents/"} {
		mux.Handle(p, people)
	}
	mux.Handle("/", Handler(e.Store, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, bridge.Actor{Name: "cli:root", Via: "cli"}, "operator", "")
	if _, err := e.Bridge.Join(ctx, strings.SplitN(inv.URL, "code=", 2)[1], "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.Machine{{Name: "box", Address: "box.example.com:22", User: "beacon"}, {Name: "ca", Address: "ca.example.com:22", User: "beacon", Critical: true}} {
		if err := e.Store.PutMachine(m); err != nil {
			t.Fatal(err)
		}
	}

	out, err := c.CreateToken(ctx, TokenRequest{Name: "plain", Scopes: []string{"status:read", "machines:ssh"}, TTL: "1h"})
	if err != nil || out.Token.Access != store.AccessNamed || len(out.Token.Machines) != 0 {
		t.Fatalf("a token made without machines: %+v %v", out.Token, err)
	}
	if p, _ := e.Store.Authenticate(out.Secret); p.Reaches("box") {
		t.Error("a token made without machines reaches one")
	}
	info, err := c.SetTokenMachines(ctx, "plain", []string{"all", "ca"})
	if err != nil || info.Access != store.AccessAll || len(info.Machines) != 1 || info.Until["ca"].IsZero() {
		t.Fatalf("all and a critical machine: %+v %v", info, err)
	}
	if !strings.Contains(info.Summary, "critical machines named: ca (until") {
		t.Errorf("the summary: %q", info.Summary)
	}
	if info, err = c.SetTokenMachines(ctx, "plain", []string{"all", "ca@never"}); err != nil || !info.Until["ca"].IsZero() {
		t.Errorf("an end of never: %+v %v", info, err)
	}
	if _, err := c.SetTokenMachines(ctx, "plain", []string{"ca@soon"}); err == nil {
		t.Error("an end the command line cannot read")
	}

	a, err := c.AddAgent(ctx, AgentRequest{Name: "ci", Owner: "bob", Preset: "oncall", Machines: []string{"all"}, TTL: "1h"})
	if err != nil || a.Agent.Access != store.AccessAll {
		t.Fatalf("an agent with all: %+v %v", a.Agent, err)
	}
	v, err := c.SetAgentMachines(ctx, "ci", []string{"all", "ca@72h"})
	if err != nil || time.Until(v.Until["ca"]) > 73*time.Hour {
		t.Fatalf("an agent's critical machine for 72 hours: %+v %v", v, err)
	}
	if p, _ := e.Store.Authenticate(a.Secret); p.Reaches("ca") {
		t.Error("an agent reaches a critical machine its owner does not")
	}
	u, err := c.SetUserMachines(ctx, "bob", []string{"ca"})
	if err != nil || len(u.Machines) != 1 || len(u.Until) != 0 {
		t.Fatalf("a person's critical machine: %+v %v", u, err)
	}
	if p, _ := e.Store.Authenticate(a.Secret); !p.Reaches("ca") {
		t.Error("an agent within its owner's critical machine")
	}
	if _, err := c.SetUserMachines(ctx, "bob", []string{"all"}); err == nil {
		t.Error("all for a person")
	}
	if u, err := c.SetUserMachines(ctx, "bob", []string{"none"}); err != nil || len(u.Machines) != 0 {
		t.Errorf("none for a person: %+v %v", u, err)
	}
}
