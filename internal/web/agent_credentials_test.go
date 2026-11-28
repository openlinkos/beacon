package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// TestConsoleAgentCredentialsStayWithinTheCaller: signed in with a token that may administer accounts but
// holds less than an owner's agent may use, the console gives that agent no new token and no key; the
// owner does.
func TestConsoleAgentCredentialsStayWithinTheCaller(t *testing.T) {
	e := newEnv(t, i18n.EN)
	alice := e.owner()
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	if _, err := e.b.CreateAgent(context.Background(), cli, bridge.AgentInput{Name: "deploy", Owner: "alice", Preset: "operator", Machines: store.AllAccess()}); err != nil {
		t.Fatal(err)
	}
	admin := e.signedIn("accounts-admin", "status:read", "accounts:write")
	for path, form := range map[string]url.Values{
		"/console/agents/deploy/tokens": {"expires": {"30d"}},
		"/console/agents/deploy/keys":   {"key": {"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}},
	} {
		if a := admin.post(path, form); a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "error=forbidden") {
			t.Errorf("%s from a token holding less than the agent: %d %q", path, a.code, a.header.Get("Location"))
		}
	}
	if a, _ := e.st.Agent("deploy"); len(a.SSHKeys) != 0 || len(e.st.AgentTokens("deploy")) != 0 {
		t.Errorf("the agent was given a credential: %d keys, %d tokens", len(a.SSHKeys), len(e.st.AgentTokens("deploy")))
	}
	if a := alice.post("/console/agents/deploy/tokens", url.Values{"expires": {"30d"}}); a.code != http.StatusCreated && a.code != http.StatusOK {
		t.Errorf("the owner gives her agent a token: %d %q", a.code, a.header.Get("Location"))
	}
	if n := len(e.st.AgentTokens("deploy")); n != 1 {
		t.Errorf("the owner's token: %d tokens", n)
	}
}
