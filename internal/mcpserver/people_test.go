package mcpserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestMain(m *testing.M) {
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

// TestKeyAndPeopleTools: an agent registers its own SSH key, shown to its owner; an owner's credential
// lists people and invites; nothing else does.
func TestKeyAndPeopleTools(t *testing.T) {
	e := testutil.New(t)
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	out, err := e.Bridge.CreateAgent(ctx, cli, bridge.AgentInput{Name: "claude-code", Owner: "alice", Preset: "oncall", TTL: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(e.Bridge, SSH{}, &api.Auth{Store: e.Store}, slog.Default(),
		WithResourceMetadata(func(*http.Request) string {
			return "https://beacon.example.com/.well-known/oauth-protected-resource/mcp"
		})))
	defer srv.Close()

	// a 401 says where to sign in
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="https://beacon.example.com/.well-known/oauth-protected-resource/mcp"`) {
		t.Errorf("401: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	cs := connect(t, srv.URL, out.Secret)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewPublicKey(pub)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
	res, text := call(t, cs, "add_ssh_key", map[string]any{"key": line, "comment": "on alice's laptop"})
	if res.IsError || !strings.Contains(text, "SHA256:") || !strings.Contains(text, "on alice's laptop") {
		t.Fatalf("add_ssh_key: %s", text)
	}
	if a, _ := e.Store.Agent("claude-code"); len(a.SSHKeys) != 1 {
		t.Errorf("the agent's keys: %+v", a.SSHKeys)
	}
	if !strings.Contains(strings.Join(e.Bot.Texts, "\n"), "alice / claude-code added SSH key "+ssh.FingerprintSHA256(k)+" to alice / claude-code via MCP") {
		t.Errorf("chat: %v", e.Bot.Texts)
	}
	if res, text := call(t, cs, "add_ssh_key", map[string]any{"key": "-----BEGIN OPENSSH PRIVATE KEY-----"}); !res.IsError || !strings.Contains(text, "not a public key") {
		t.Errorf("a private key: %s", text)
	}
	if _, text := call(t, cs, "list_ssh_keys", nil); !strings.Contains(text, ssh.FingerprintSHA256(k)) {
		t.Errorf("list_ssh_keys: %s", text)
	}
	if _, text := call(t, cs, "whoami", nil); !strings.Contains(text, `"actor":"alice / claude-code"`) || !strings.Contains(text, ssh.FingerprintSHA256(k)) {
		t.Errorf("whoami: %s", text)
	}
	if res, text := call(t, cs, "list_users", nil); !res.IsError || !strings.Contains(text, "alice / claude-code may not use the accounts:write scope") {
		t.Errorf("an agent lists people: %s", text)
	}
	if res, _ := call(t, cs, "remove_ssh_key", map[string]any{"fingerprint": ssh.FingerprintSHA256(k)}); res.IsError {
		t.Error("remove_ssh_key")
	}
	if a, _ := e.Store.Agent("claude-code"); len(a.SSHKeys) != 0 {
		t.Errorf("the key outlives removing it: %+v", a.SSHKeys)
	}

	// an owner's credential (a token made on the host with accounts:write) lists people and invites
	owner := connect(t, srv.URL, e.Token(t, "owner-script", "status:read", "accounts:write"))
	if _, text := call(t, owner, "list_users", nil); !strings.Contains(text, `"username":"alice"`) || strings.Contains(text, "argon2") {
		t.Errorf("list_users: %s", text)
	}
	res, text = call(t, owner, "invite_user", map[string]any{"role": "oncall", "note": "bob"})
	if res.IsError || !strings.Contains(text, "/console/join?code=") {
		t.Fatalf("invite_user: %s", text)
	}
	if res, _ := call(t, owner, "invite_user", map[string]any{"role": "emperor"}); !res.IsError {
		t.Error("an invitation with an unknown role")
	}
}
