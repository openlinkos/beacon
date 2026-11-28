package mcpserver

import (
	"strings"
	"testing"
)

// TestResumeSSHTakesAPerson: pause_ssh works for a token with machines:write; resume_ssh does not, and
// the channel stays paused.
func TestResumeSSHTakesAPerson(t *testing.T) {
	e, u := lockMCP(t)
	op := connect(t, u, e.Token(t, "op-script", "status:read", "machines:write"))
	if res, text := call(t, op, "pause_ssh", map[string]any{}); res.IsError {
		t.Fatalf("pause_ssh: %s", text)
	}
	if res, text := call(t, op, "resume_ssh", map[string]any{}); !res.IsError || !strings.Contains(text, "only a person") {
		t.Errorf("a token resumes: %v %s", res.IsError, text)
	}
	if res, text := call(t, op, "get_ssh_status", map[string]any{}); res.IsError || !strings.Contains(text, `"paused":true`) {
		t.Errorf("after a refused resume: %s", text)
	}
}
