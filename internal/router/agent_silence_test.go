package router

import (
	"context"
	"testing"
	"time"
)

// TestAgentSilenceMutesNothing checks that an agent's silence is kept and listed but lets what it
// matches through, so that people are still notified of it.
func TestAgentSilenceMutesNothing(t *testing.T) {
	e := newEnv(t, base)
	id, err := e.r.CreateAgentSilence(context.Background(), map[string]string{"service": "git"}, e.now, e.now.Add(time.Hour), "bob / claude-code", "expected")
	if err != nil {
		t.Fatal(err)
	}
	e.fire(down("git"), down("hub"))
	e.tick(31 * time.Second)
	if hooks := e.s.take(); len(hooks) != 2 {
		t.Fatalf("an agent's silence muted alerts: %+v", hooks)
	}
	ss, _ := e.r.Silences(context.Background())
	if len(ss) != 1 || ss[0].ID != id || ss[0].Status.State != "active" || ss[0].CreatedBy != "bob / claude-code" {
		t.Fatalf("the agent's silence is not listed: %+v", ss)
	}
	if err := e.r.ExpireSilence(context.Background(), id); err != nil {
		t.Errorf("ending an agent's silence: %v", err)
	}
}
