package sshgate

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

// open starts a long command through the channel with key and returns a channel that is closed when the
// session ends, however it ends.
func (e *env) open(t *testing.T, key ssh.Signer) <-chan struct{} {
	t.Helper()
	e.waitOpen(t, 0) // what was closed before is gone
	cl, err := e.client(t, "box", key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = s.Run("sleep 30")
		close(done)
	}()
	e.waitOpen(t, 1)
	return done
}

// waitOpen waits until exactly n things are open on the channel.
func (e *env) waitOpen(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.g.mu.Lock()
		got := len(e.g.open)
		e.g.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d open on the channel, want %d", got, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ended(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the session outlived %s", what)
	}
}

func alive(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("the session ended with %s", what)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRecheckClosesSessions: revoking a token, narrowing or deleting an agent, disabling or demoting its
// owner, revoking its OAuth grant or removing its key closes what it has open on the channel at once;
// what may still reach its machine stays open.
func TestRecheckClosesSessions(t *testing.T) {
	needShell(t)
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	e := setup(t)

	// a legacy token's session ends when the token is revoked
	done := e.open(t, e.agentKey)
	if n := e.g.Recheck(); n != 0 {
		t.Fatalf("Recheck closed %d with nothing changed", n)
	}
	alive(t, done, "a recheck")
	if err := e.st.RevokeToken("claude-code"); err != nil {
		t.Fatal(err)
	}
	if n := e.g.Recheck(); n != 1 {
		t.Errorf("Recheck closed %d, want 1", n)
	}
	ended(t, done, "revoking its token")

	// an agent: owned by bob, signing in by its key, with a token as its credential
	code, _, _ := e.st.CreateSetupLink()
	if _, err := e.st.Join(code, "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := e.st.CreateInvitation("operator", "", "alice")
	if _, err := e.st.Join(inv, "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateAgent(store.Agent{Name: "codex", Owner: "bob", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	_, tok, err := e.st.CreateAgentToken("codex", 24*time.Hour, "bob")
	if err != nil {
		t.Fatal(err)
	}
	_, tok2, _ := e.st.CreateAgentToken("codex", 24*time.Hour, "bob")
	key := signer(t)
	fp := ssh.FingerprintSHA256(key.PublicKey())
	if _, err := e.st.AddAgentSSHKey("codex", store.SSHKey{Key: string(ssh.MarshalAuthorizedKey(key.PublicKey())), Fingerprint: fp}); err != nil {
		t.Fatal(err)
	}

	// fewer machines: closed; and it may not open another
	done = e.open(t, key)
	if _, err := e.st.UpdateAgent("codex", func(a *store.Agent) error { a.Machines = []string{"other"}; return nil }); err != nil {
		t.Fatal(err)
	}
	e.g.Recheck()
	ended(t, done, "losing the machine")
	if _, errOut, code := e.runWith(t, key, "true"); code != 1 || errOut == "" {
		t.Errorf("after losing the machine: %q %d", errOut, code)
	}
	_, _ = e.st.UpdateAgent("codex", func(a *store.Agent) error { a.Machines = nil; return nil })

	// one token revoked of two: the agent still has a credential, its key session stays
	done = e.open(t, key)
	if err := e.st.RevokeToken(tok.Name); err != nil {
		t.Fatal(err)
	}
	e.g.Recheck()
	alive(t, done, "revoking one of two tokens")
	// its owner demoted below machines:ssh: closed
	if _, err := e.st.UpdateAccount("bob", func(a *store.Account) error { a.Role = "viewer"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.g.Recheck()
	ended(t, done, "demoting its owner")
	_, _ = e.st.UpdateAccount("bob", func(a *store.Account) error { a.Role = "operator"; return nil })

	// its owner disabled: closed
	done = e.open(t, key)
	_, _ = e.st.UpdateAccount("bob", func(a *store.Account) error { a.Disabled = true; return nil })
	e.g.Recheck()
	ended(t, done, "disabling its owner")
	_, _ = e.st.UpdateAccount("bob", func(a *store.Account) error { a.Disabled = false; return nil })

	// its key removed: closed
	done = e.open(t, key)
	if _, err := e.st.RemoveAgentSSHKey("codex", fp); err != nil {
		t.Fatal(err)
	}
	e.g.Recheck()
	ended(t, done, "removing its key")
	_, _ = e.st.AddAgentSSHKey("codex", store.SSHKey{Key: string(ssh.MarshalAuthorizedKey(key.PublicKey())), Fingerprint: fp})

	// a tool call made with an OAuth grant: revoking the grant cancels it; one made with a token, the token
	if _, err := e.st.RegisterClient(store.OAuthClient{Name: "Claude Code", RedirectURIs: []string{"http://localhost:1/callback"}}); err != nil {
		t.Fatal(err)
	}
	in, err := e.st.CreateGrant(store.Grant{Agent: "codex", Client: "c", ClientName: "Claude Code", Expires: time.Now().Add(time.Hour)}, false)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := e.st.Authenticate(in.Access)
	if !ok {
		t.Fatal("the access token does not work")
	}
	who := Actor{Name: p.Name(), Via: "mcp", Key: p.Key(), Cred: p.Cred()}
	e.waitOpen(t, 0)
	res := make(chan error, 1)
	go func() {
		_, err := e.g.Run(context.Background(), who, "box", RunInput{Command: "sleep 30", Timeout: 60})
		res <- err
	}()
	e.waitOpen(t, 1)
	if err := e.st.RevokeGrant(in.Grant.ID); err != nil {
		t.Fatal(err)
	}
	e.g.Recheck()
	select {
	case <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool call outlived its grant")
	}

	// deleting the agent ends its key's session too
	done = e.open(t, key)
	if err := e.st.DeleteAgent("codex"); err != nil {
		t.Fatal(err)
	}
	e.g.Recheck()
	ended(t, done, "deleting the agent")
	if _, err := e.client(t, "box", key); err == nil {
		t.Error("a deleted agent's key signs in")
	}
	_ = tok2
}

// runWith is run with another key.
func (e *env) runWith(t *testing.T, key ssh.Signer, cmd string) (string, string, int) {
	t.Helper()
	old := e.agentKey
	e.agentKey = key
	defer func() { e.agentKey = old }()
	out, errOut, code := e.run(t, "box", cmd)
	return out, errOut, code
}
