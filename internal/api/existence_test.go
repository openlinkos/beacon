package api

import (
	"strings"
	"testing"
	"time"
)

// TestAgentNamesNeedTheRight checks that a credential that manages no agents gets the same answer for
// an agent that exists as for one that does not.
func TestAgentNamesNeedTheRight(t *testing.T) {
	e, c := setup(t)
	people(t, e) // bob's agent codex
	probe, _, err := e.Store.CreateSubjectToken("probe-edge-1", "edge-1", []string{"probe:write"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, cred := range []string{probe, e.Token(t, "reader", "status:read")} {
		for _, method := range []string{"GET", "PATCH"} {
			body := ""
			if method == "PATCH" {
				body = `{"preset": "readonly"}`
			}
			code1, out1 := c.do(method, "/api/v1/agents/codex", cred, body)
			code2, out2 := c.do(method, "/api/v1/agents/no-such-agent", cred, body)
			if code1 != code2 || out1["code"] != out2["code"] || code1 != 403 {
				t.Errorf("%s: an agent that exists %d %v, one that does not %d %v", method, code1, out1, code2, out2)
			}
		}
	}
}

// TestKeyConflictNamesNoHolder checks that registering a key someone else holds says only that it is
// registered.
func TestKeyConflictNamesNoHolder(t *testing.T) {
	e, c := setup(t)
	agent := people(t, e)
	key := pubKey(t)
	if code, out := c.do("POST", "/api/v1/ssh-keys", e.Token(t, "ops-laptop", "status:read", "machines:ssh"), `{"key": "`+key+`"}`); code != 201 {
		t.Fatalf("the first holder: %d %v", code, out)
	}
	code, out := c.do("POST", "/api/v1/ssh-keys", agent, `{"key": "`+key+`"}`)
	msg, _ := out["error"].(string)
	if code != 409 || out["code"] != "key.exists" || !strings.Contains(msg, "already registered") || strings.Contains(msg, "ops-laptop") || strings.Contains(msg, "token") {
		t.Errorf("a key held by another: %d %v", code, out)
	}
}

// TestTokenRefusalsAlike checks that revoking a token that does not exist, and one the caller may not
// revoke here, get the same answer.
func TestTokenRefusalsAlike(t *testing.T) {
	e, c := setup(t)
	writer := e.Token(t, "machines-admin", "status:read", "machines:write")
	if _, _, err := e.Store.CreateSubjectToken("probe-edge-1", "edge-1", []string{"probe:write"}, time.Hour, "test"); err != nil {
		t.Fatal(err)
	}
	code1, out1 := c.do("DELETE", "/api/v1/tokens/probe-edge-1", writer, "")
	code2, out2 := c.do("DELETE", "/api/v1/tokens/probe-edge-9", writer, "")
	same := func(s any) string { return strings.ReplaceAll(s.(string), "probe-edge-9", "probe-edge-1") }
	if code1 != code2 || out1["code"] != out2["code"] || out1["error"] != same(out2["error"]) || code1 != 404 {
		t.Errorf("a token the caller may not revoke %d %v, one that does not exist %d %v", code1, out1, code2, out2)
	}
	for _, tok := range e.Store.Tokens() {
		if tok.Name == "probe-edge-1" {
			return
		}
	}
	t.Error("the machine's token was revoked")
}
