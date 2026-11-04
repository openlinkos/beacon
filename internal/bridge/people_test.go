package bridge_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
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

func key(t *testing.T) (string, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), ssh.FingerprintSHA256(k)
}

// TestPeopleOperations: who may act on whose agents, what reaches the audit log and the chat, and that
// a change that ends access rechecks the SSH channel.
func TestPeopleOperations(t *testing.T) {
	rechecks := 0
	e := testutil.NewWith(t, func(d *bridge.Deps) { d.Recheck = func() int { rechecks++; return 0 } })
	ctx := context.Background()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	code, _, _ := e.Store.CreateSetupLink()
	if _, err := e.Bridge.Join(ctx, code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _ := e.Bridge.Invite(ctx, cli, "oncall", "")
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	bob := bridge.Actor{Name: "bob", Via: "console", Key: "user:bob"}
	carol := bridge.Actor{Name: "carol", Via: "console", Key: "user:carol"}

	// a key that is a machine's connector key: no agent half made
	line, fp := key(t)
	if err := e.Store.PutMachine(store.Machine{Name: "nat-box", Address: store.ConnectorAddress, User: "beacon", ConnectorKey: line, ConnectorFingerprint: fp}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "ci", SSHKeys: []string{line}, TTL: time.Hour}); !errors.Is(err, bridge.ErrBadInput) {
		t.Errorf("an agent with a connector key: %v", err)
	}
	if _, ok := e.Store.Agent("ci"); ok {
		t.Error("an agent was half made")
	}

	// bob makes his own agent; the owner defaults to him and the preset to his role's strongest
	out, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "ci", TTL: 30 * 24 * time.Hour})
	if err != nil || out.Agent.Owner != "bob" || out.Agent.Preset != "oncall" || out.Secret == "" {
		t.Fatalf("bob's agent: %+v %v", out, err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "x2", Owner: "alice", TTL: time.Hour}); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("bob makes alice's agent: %v", err)
	}
	if _, err := e.Bridge.CreateAgent(ctx, bob, bridge.AgentInput{Name: "x3", Preset: store.PresetCustom, Scopes: []string{"status:read"}, TTL: time.Hour}); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("custom scopes from the console: %v", err)
	}
	// carol is nobody: she may not touch bob's agent
	if err := e.Bridge.DeleteAgent(ctx, carol, "ci"); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("carol deletes bob's agent: %v", err)
	}
	if _, err := e.Bridge.Agents(carol, ""); !errors.Is(err, bridge.ErrForbidden) {
		t.Errorf("an unknown principal lists agents: %v", err)
	}
	// an agent's own token adds its own key, marked with that token
	p, _ := e.Store.Authenticate(out.Secret)
	agent := bridge.ActorOf(p, "mcp")
	line, fp = key(t)
	k, err := e.Bridge.AddSSHKey(ctx, agent, "", line+" laptop", "")
	if err != nil || k.Comment != "laptop" || k.Via != "token:ci" {
		t.Fatalf("the agent's own key: %+v %v", k, err)
	}
	if _, err := e.Bridge.AddSSHKey(ctx, agent, "other", line, ""); err == nil {
		t.Error("an agent adds a key to another agent")
	}
	// revoking the token rechecks the channel and takes the key with it
	before := rechecks
	if err := e.Bridge.RevokeAgentToken(ctx, bob, "ci"); err != nil {
		t.Fatal(err)
	}
	if rechecks == before {
		t.Error("revoking did not recheck the SSH channel")
	}
	if a, _ := e.Store.Agent("ci"); len(a.SSHKeys) != 0 {
		t.Errorf("the key outlives its token: %+v", a.SSHKeys)
	}
	_ = fp
	// demoting bob rechecks too
	before = rechecks
	role := "viewer"
	if _, err := e.Bridge.UpdateUser(ctx, cli, "bob", bridge.UserChange{Role: &role}); err != nil {
		t.Fatal(err)
	}
	if rechecks == before {
		t.Error("a demotion did not recheck the SSH channel")
	}

	// the audit log and the chat name people and their agents
	found := map[string]bool{}
	for _, en := range e.Store.AuditLog(50) {
		found[en.Actor+" "+en.Action] = true
	}
	for _, want := range []string{"bob agent_create", "bob agent_token", "bob / ci ssh_key_add", "bob revoke_token", "cli:root user_role", "cli:root user_invite", "alice setup", "bob user_join"} {
		if !found[want] {
			t.Errorf("no %q in the audit log: %v", want, found)
		}
	}
	chat := strings.Join(e.Bot.Texts, "\n")
	for _, want := range []string{"bob made agent ci for bob (On call) via the console", "bob / ci added SSH key", "bob revoked token ci via the console", "cli:root changed the role of bob to Viewer via the command line"} {
		if !strings.Contains(chat, want) {
			t.Errorf("the chat lacks %q:\n%s", want, chat)
		}
	}
	if strings.Contains(chat, out.Secret) || strings.Contains(chat, "code=") {
		t.Error("a secret or a link reached the chat")
	}
}
