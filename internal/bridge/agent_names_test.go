package bridge_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// TestAgentNamesNeedTheRight checks that whoever may not manage an agent gets the same refusal for
// another person's agent as for a name no agent has, on every operation that takes a name, while an
// admin is told that an agent does not exist.
func TestAgentNamesNeedTheRight(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bob", "carol"} {
		inv, _ := e.Bridge.Invite(ctx, cli, "oncall", "")
		u, _ := url.Parse(inv.URL)
		if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), name, "", "correct horse battery"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "bobs-agent", Owner: "bob", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	carol := bridge.Actor{Name: "carol", Via: "console", Key: "user:carol"}
	machine := bridge.Actor{Name: "probe-edge-1", Via: "api", Key: "token:probe-edge-1"}
	preset := "readonly"
	ops := map[string]func(bridge.Actor, string) error{
		"view": func(who bridge.Actor, n string) error { _, err := e.Bridge.Agent(who, n); return err },
		"update": func(who bridge.Actor, n string) error {
			_, err := e.Bridge.UpdateAgent(ctx, who, n, bridge.AgentUpdate{Preset: &preset})
			return err
		},
		"token": func(who bridge.Actor, n string) error {
			_, err := e.Bridge.NewAgentToken(ctx, who, n, time.Hour)
			return err
		},
		"delete": func(who bridge.Actor, n string) error { return e.Bridge.DeleteAgent(ctx, who, n) },
		"keys":   func(who bridge.Actor, n string) error { _, err := e.Bridge.SSHKeys(who, n); return err },
	}
	for op, do := range ops {
		for _, who := range []bridge.Actor{carol, machine} {
			exists, missing := do(who, "bobs-agent"), do(who, "no-such-agent")
			if !errors.Is(exists, bridge.ErrForbidden) || !errors.Is(missing, bridge.ErrForbidden) || errors.Is(missing, store.ErrNotFound) {
				t.Errorf("%s by %s: for an agent that exists %v, for one that does not %v", op, who.Name, exists, missing)
			}
		}
		if err := do(cli, "no-such-agent"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s by an admin of an agent that does not exist: %v", op, err)
		}
	}
	if _, ok := e.Store.Agent("bobs-agent"); !ok {
		t.Fatal("a refused caller deleted the agent")
	}
}
