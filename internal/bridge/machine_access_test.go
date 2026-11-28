package bridge_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestWhoGivesMachineAccess: only a person or the command line gives access to all machines; only an
// owner or the command line names a critical machine, for an agent or a person; a critical machine
// named for an agent ends after 30 days unless told otherwise.
func TestWhoGivesMachineAccess(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, cli, "operator", "")
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.Machine{{Name: "box", Address: "box.example.com:22", User: "beacon"}, {Name: "ca", Address: "ca.example.com:22", User: "beacon", Critical: true}} {
		if err := e.Store.PutMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := e.Store.AccountPrincipal("alice")
	bob, _ := e.Store.AccountPrincipal("bob")
	owner, operator := bridge.ActorOf(alice, "console"), bridge.ActorOf(bob, "console")
	admin := e.Token(t, "admin", append(store.RoleScopes("operator"), "accounts:write")...)
	ap, _ := e.Store.Authenticate(admin)
	script := bridge.ActorOf(ap, "api")

	// a token, even an administrator's with access to all machines, gives no agent all machines
	if _, err := e.Bridge.CreateAgent(ctx, script, bridge.AgentInput{Name: "wide", Owner: "bob", Preset: "operator", Machines: store.AllAccess()}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "only a person") {
		t.Errorf("a token gives all machines: %v", err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, script, bridge.AgentInput{Name: "narrow", Owner: "bob", Preset: "operator", Machines: store.NamedAccess("box")}); err != nil {
		t.Errorf("a token names a machine it reaches: %v", err)
	}
	// a person who is not an owner names no critical machine, for their agent or anyone
	if _, err := e.Bridge.CreateAgent(ctx, operator, bridge.AgentInput{Name: "bobs", Preset: "operator", Machines: store.AllAccess("ca")}); !errors.Is(err, bridge.ErrForbidden) || !strings.Contains(err.Error(), "critical") {
		t.Errorf("an operator names a critical machine: %v", err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, operator, bridge.AgentInput{Name: "bobs", Preset: "operator", Machines: store.AllAccess()}); err != nil {
		t.Errorf("an operator gives all machines: %v", err)
	}
	if _, err := e.Bridge.SetCriticalMachines(ctx, operator, "bob", store.NamedAccess("ca")); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an operator names a critical machine on an account: %v", err)
	}
	if _, err := e.Bridge.SetCriticalMachines(ctx, script, "bob", store.NamedAccess("ca")); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("a token names a critical machine on an account: %v", err)
	}
	// an owner names it, for an agent for 30 days unless told otherwise
	out, err := e.Bridge.CreateAgent(ctx, owner, bridge.AgentInput{Name: "alices", Preset: "operator", Machines: store.AllAccess("ca")})
	if err != nil {
		t.Fatalf("an owner names a critical machine: %v", err)
	}
	if until := out.Agent.Until["ca"]; until.IsZero() || time.Until(until) > store.CriticalGrantTTL+time.Minute || time.Until(until) < store.CriticalGrantTTL-time.Hour {
		t.Errorf("the critical machine's end: %v", until)
	}
	forever := store.AllAccess("ca")
	if _, err := e.Bridge.UpdateAgent(ctx, cli, "alices", bridge.AgentUpdate{Machines: &forever, Forever: []string{"ca"}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.Store.Agent("alices"); !a.Until("ca").IsZero() {
		t.Errorf("the command line gives no end: %v", a.Until("ca"))
	}
	// and for a person, without an end; their agents follow
	if _, err := e.Bridge.SetCriticalMachines(ctx, owner, "bob", store.NamedAccess("ca", "nope")); !errors.Is(err, bridge.ErrBadInput) {
		t.Errorf("a machine that is not enrolled: %v", err)
	}
	v, err := e.Bridge.SetCriticalMachines(ctx, owner, "bob", store.NamedAccess("ca"))
	if err != nil || v.Access != store.AccessAll || len(v.Machines) != 1 || len(v.Until) != 0 {
		t.Fatalf("an owner names a critical machine on an account: %+v %v", v, err)
	}
	if !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "set the critical machines bob reaches") {
		t.Errorf("chat: %v", e.Bot.Texts)
	}
	if bob, _ := e.Store.AccountPrincipal("bob"); !bob.Reaches("ca") {
		t.Error("the person does not reach the critical machine named on them")
	}
	// a person who reaches a critical machine still does not pass it on
	bob, _ = e.Store.AccountPrincipal("bob")
	operator = bridge.ActorOf(bob, "console")
	both := store.AllAccess("ca")
	if _, err := e.Bridge.UpdateAgent(ctx, operator, "bobs", bridge.AgentUpdate{Machines: &both}); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an operator passes on a critical machine: %v", err)
	}
}
