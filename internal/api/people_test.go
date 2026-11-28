package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestMain(m *testing.M) {
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

func pubKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// people gives the env alice, an owner, and bob, on call, with an agent and its token.
func people(t *testing.T, e *testutil.Env) (agentToken string) {
	t.Helper()
	ctx := context.Background()
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, bridge.Actor{Name: "cli:root", Via: "cli"}, "oncall", "")
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	out, err := e.Bridge.CreateAgent(ctx, bridge.Actor{Name: "cli:root", Via: "cli"}, bridge.AgentInput{Name: "codex", Owner: "bob", Preset: "oncall", Machines: store.AllAccess(), TTL: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return out.Secret
}

func TestPeopleRoutes(t *testing.T) {
	e, c := setup(t)
	agent := people(t, e)
	owner := e.Token(t, "owner-script", store.PeopleScopes...)
	weak := e.Token(t, "accounts-script", "status:read", "accounts:write")

	// a token is held to the roles within its own scopes: accounts:write alone invites, resets or
	// changes nobody
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/api/v1/users/invitations", `{"role": "viewer"}`},
		{"POST", "/api/v1/users/bob/reset", ""},
		{"PATCH", "/api/v1/users/bob", `{"display_name": "Bob"}`},
		{"PATCH", "/api/v1/users/alice", `{"disabled": true}`},
	} {
		if code, out := c.do(req.method, req.path, weak, req.body); code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "which you do not hold") {
			t.Errorf("%s %s with accounts:write alone: %d %v", req.method, req.path, code, out)
		}
	}

	// accounts:write only: an agent never has it (its presets stop at operator)
	if code, out := c.do("GET", "/api/v1/users", agent, ""); code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "bob / codex may not use the accounts:write scope") {
		t.Errorf("an agent lists people: %d %v", code, out)
	}
	code, out := c.do("GET", "/api/v1/users", owner, "")
	if code != 200 || len(out["users"].([]any)) != 2 || strings.Contains(strings.ToLower(jsonText(out)), "password") {
		t.Fatalf("users: %d %v", code, out)
	}
	code, out = c.do("POST", "/api/v1/users/invitations", owner, `{"role": "viewer", "note": "carol"}`)
	if code != 201 || !strings.Contains(out["url"].(string), "/console/join?code=") || out["role"] != "viewer" {
		t.Errorf("invite: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/users/invitations", owner, `{"role": "root"}`); code != 400 || out["code"] != "bad_input" {
		t.Errorf("an unknown role: %d %v", code, out)
	}
	if code, out := c.do("PATCH", "/api/v1/users/bob", owner, `{"role": "viewer"}`); code != 200 || out["role"] != "viewer" {
		t.Errorf("role: %d %v", code, out)
	}
	// bob's agent is limited at once
	if code, out := c.do("POST", "/api/v1/alerts/"+testutil.GroupID()+"/notes", agent, `{"text": "x"}`); code != 403 || !strings.Contains(out["error"].(string), "within its owner's role allows status:read, sessions:read") {
		t.Errorf("a demoted owner's agent: %d %v", code, out)
	}
	// an owner is acted on only by an owner or the command line, never by a token
	if code, out := c.do("PATCH", "/api/v1/users/alice", owner, `{"disabled": true}`); code != 403 || out["code"] != "auth.scope" || !strings.Contains(out["error"].(string), "owner role") {
		t.Errorf("an owner disabled by a token: %d %v", code, out)
	}
	if code, out := c.do("PATCH", "/api/v1/users/nobody", owner, `{"disabled": true}`); code != 404 || out["code"] != "user.not_found" {
		t.Errorf("nobody: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/users/bob/reset", owner, ""); code != 201 || !strings.Contains(out["url"].(string), "/console/reset?code=") {
		t.Errorf("reset: %d %v", code, out)
	}
	// the pending reset link is listed without its code, and withdrawn
	code, out = c.do("GET", "/api/v1/users", owner, "")
	resets, _ := out["reset_links"].([]any)
	if code != 200 || len(resets) != 1 || strings.Contains(jsonText(out), "code=") {
		t.Fatalf("pending reset links: %d %v", code, out)
	}
	id := resets[0].(map[string]any)["id"].(string)
	if code, out := c.do("DELETE", "/api/v1/users/links/"+id, weak, ""); code != 200 || out["kind"] != "reset" || out["user"] != "bob" {
		t.Errorf("withdraw: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/users/links/"+id, owner, ""); code != 404 || out["code"] != "link.not_found" {
		t.Errorf("withdraw twice: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/users/links/"+id, agent, ""); code != 403 || out["code"] != "auth.scope" {
		t.Errorf("an agent withdraws: %d %v", code, out)
	}
	if !hasEntry(e.Store.AuditLog(20), "accounts-script", "user_reset_withdraw", "bob") {
		t.Errorf("audit: %+v", e.Store.AuditLog(20))
	}
	for _, s := range e.Bot.Texts {
		if strings.Contains(s, "code=") {
			t.Errorf("a link reached the chat: %s", s)
		}
	}
	if !hasEntry(e.Store.AuditLog(20), "owner-script", "user_role", "bob") {
		t.Errorf("audit: %+v", e.Store.AuditLog(20))
	}
}

func TestSSHKeyRoutes(t *testing.T) {
	e, c := setup(t)
	agent := people(t, e)
	key := pubKey(t)
	code, out := c.do("POST", "/api/v1/ssh-keys", agent, `{"key": "`+key+` codex@laptop"}`)
	if code != 201 || out["comment"] != "codex@laptop" || !strings.HasPrefix(out["fingerprint"].(string), "SHA256:") {
		t.Fatalf("add: %d %v", code, out)
	}
	fp := out["fingerprint"].(string)
	if a, _ := e.Store.Agent("codex"); len(a.SSHKeys) != 1 {
		t.Errorf("the key is not the agent's: %+v", a)
	}
	if code, out := c.do("GET", "/api/v1/ssh-keys", agent, ""); code != 200 || len(out["keys"].([]any)) != 1 {
		t.Errorf("list: %d %v", code, out)
	}
	// a key belongs to one: another token cannot take it
	legacy := e.Token(t, "legacy", "status:read", "machines:ssh")
	if code, out := c.do("POST", "/api/v1/ssh-keys", legacy, `{"key": "`+key+`"}`); code != 409 || out["code"] != "key.exists" {
		t.Errorf("a key held by another: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/ssh-keys", legacy, `{"key": "ssh-rsa nope"}`); code != 400 || out["code"] != "bad_input" {
		t.Errorf("a bad key: %d %v", code, out)
	}
	// a legacy token keeps its own keys
	if code, _ := c.do("POST", "/api/v1/ssh-keys", legacy, `{"key": "`+pubKey(t)+`"}`); code != 201 {
		t.Errorf("a legacy token's key: %d", code)
	}
	reader := e.Token(t, "reader", "status:read")
	if code, _ := c.do("POST", "/api/v1/ssh-keys", reader, `{"key": "`+pubKey(t)+`"}`); code != 403 {
		t.Errorf("without machines:ssh: %d", code)
	}
	if code, out := c.do("DELETE", "/api/v1/ssh-keys?fingerprint=nope", agent, ""); code != 404 || out["code"] != "key.not_found" {
		t.Errorf("remove an unknown key: %d %v", code, out)
	}
	if code, _ := c.do("DELETE", "/api/v1/ssh-keys?fingerprint="+url.QueryEscape(fp), agent, ""); code != 200 {
		t.Errorf("remove: %d", code)
	}
	if !hasEntry(e.Store.AuditLog(20), "bob / codex", "ssh_key_add", "bob / codex") || !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "bob / codex added SSH key "+fp+" to bob / codex via API") {
		t.Errorf("audit and chat: %+v %v", e.Store.AuditLog(20), e.Bot.Texts)
	}

	// whoami: who the agent is, and that its token renews itself
	code, out = c.do("GET", "/api/v1/token", agent, "")
	if code != 200 || out["actor"] != "bob / codex" || out["user"] != "bob" || out["agent"] != "codex" || out["preset"] != "oncall" || out["credential"] != "token" || out["renewable"] != true {
		t.Errorf("whoami: %d %v", code, out)
	}
}

func hasEntry(entries []store.AuditEntry, actor, action, target string) bool {
	for _, e := range entries {
		if e.Actor == actor && e.Action == action && e.Target == target {
			return true
		}
	}
	return false
}

func jsonText(v map[string]any) string {
	var b strings.Builder
	for k, x := range v {
		b.WriteString(k)
		if s, ok := x.(string); ok {
			b.WriteString(s)
		}
		if l, ok := x.([]any); ok {
			for _, it := range l {
				if m, ok := it.(map[string]any); ok {
					b.WriteString(jsonText(m))
				}
			}
		}
	}
	return b.String()
}
