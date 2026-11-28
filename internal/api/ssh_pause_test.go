package api

import (
	"context"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// TestResumeTakesAPerson: whoever may pause the SSH channel pauses it; a token or an agent does not
// resume it, and capabilities say so.
func TestResumeTakesAPerson(t *testing.T) {
	e, c, _ := lockSetup(t)
	op := e.Token(t, "op-script", "status:read", "machines:write")
	made, err := e.Bridge.CreateAgent(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"},
		bridge.AgentInput{Name: "ops-agent", Owner: "alice", Preset: "operator", Machines: store.AllAccess(), TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"a token": op, "an agent": made.Secret} {
		if code, out := c.do("POST", "/api/v1/ssh/pause", tok, ""); code != 200 || out["paused"] != true {
			t.Fatalf("%s pauses: %d %v", name, code, out)
		}
		if code, out := c.do("POST", "/api/v1/ssh/resume", tok, ""); code != 403 || out["code"] != "ssh.resume_forbidden" {
			t.Errorf("%s resumes: %d %v", name, code, out)
		}
		if code, out := c.do("GET", "/api/v1/ssh", tok, ""); code != 200 || out["paused"] != true {
			t.Errorf("after %s's refused resume: %d %v", name, code, out)
		}
		_, caps := c.do("GET", "/api/v1/capabilities", tok, "")
		if available(caps, "resume_ssh") || !available(caps, "pause_ssh") {
			t.Errorf("%s: capabilities offer resume_ssh %v, pause_ssh %v", name, available(caps, "resume_ssh"), available(caps, "pause_ssh"))
		}
	}
	resumed := 0
	for _, a := range e.Store.AuditLog(100) {
		if a.Action == "ssh_resume" {
			resumed++
		}
	}
	if resumed != 0 {
		t.Errorf("a refused resume was audited %d times", resumed)
	}
}

// available reports whether the capabilities document offers the tool to its caller.
func available(caps map[string]any, tool string) bool {
	domains, _ := caps["domains"].([]any)
	for _, d := range domains {
		ops, _ := d.(map[string]any)["operations"].([]any)
		for _, o := range ops {
			op, _ := o.(map[string]any)
			if op["tool"] == tool {
				return op["available"] == true
			}
		}
	}
	return false
}
