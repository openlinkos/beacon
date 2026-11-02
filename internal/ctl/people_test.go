package ctl

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

func TestMain(m *testing.M) {
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

// TestPeopleOverTheSocket: beacon setup, user and agent commands and token claim, as the command line
// sends them over the admin socket.
func TestPeopleOverTheSocket(t *testing.T) {
	e := testutil.New(t)
	mux := http.NewServeMux()
	people := PeopleHandler(e.Bridge)
	for _, p := range []string{"/v1/setup", "/v1/users", "/v1/users/", "/v1/agents", "/v1/agents/", "/v1/claims"} {
		mux.Handle(p, people)
	}
	mux.Handle("/", Handler(e.Store, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()

	l, err := c.SetupLink(ctx)
	if err != nil || l.Kind != "setup" || !strings.Contains(l.URL, "/console/setup?code=") {
		t.Fatalf("setup: %+v %v", l, err)
	}
	u, _ := url.Parse(l.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "alice", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetupLink(ctx); err == nil || !strings.Contains(err.Error(), "accounts already") {
		t.Errorf("setup with accounts: %v", err)
	}
	inv, err := c.Invite(ctx, InviteRequest{Role: "oncall", Note: "bob"})
	if err != nil || inv.Role != "oncall" || !strings.Contains(inv.URL, "/console/join?code=") || !strings.HasPrefix(inv.CreatedBy, "cli:") {
		t.Fatalf("invite: %+v %v", inv, err)
	}
	u, _ = url.Parse(inv.URL)
	if _, err := e.Bridge.Join(ctx, u.Query().Get("code"), "bob", "", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Invite(ctx, InviteRequest{Role: "king"}); err == nil {
		t.Error("an unknown role")
	}
	list, err := c.Users(ctx)
	if err != nil || len(list.Users) != 2 {
		t.Fatalf("users: %+v %v", list, err)
	}
	role := "viewer"
	if got, err := c.UpdateUser(ctx, "bob", bridge.UserChange{Role: &role}); err != nil || got.Role != "viewer" {
		t.Errorf("role: %+v %v", got, err)
	}
	off := true
	if got, err := c.UpdateUser(ctx, "bob", bridge.UserChange{Disabled: &off}); err != nil || !got.Disabled {
		t.Errorf("disable: %+v %v", got, err)
	}
	if _, err := c.UpdateUser(ctx, "alice", bridge.UserChange{Disabled: &off}); err == nil || !strings.Contains(err.Error(), "without an owner") {
		t.Errorf("disabling the last owner: %v", err)
	}
	if r, err := c.ResetUser(ctx, "bob"); err != nil || !strings.Contains(r.URL, "/console/reset?code=") || r.User != "bob" {
		t.Errorf("reset: %+v %v", r, err)
	}

	// agents: the token and its snippets, shown once; the preset never above the owner's role
	if _, err := c.AddAgent(ctx, AgentRequest{Name: "ci", Owner: "bob", Preset: "operator", TTL: "30d"}); err == nil || !strings.Contains(err.Error(), "more than bob's role") {
		t.Errorf("a preset above the role: %v", err)
	}
	out, err := c.AddAgent(ctx, AgentRequest{Name: "ci", Owner: "alice", Preset: "oncall", Machines: []string{"edge-1"}, TTL: "30d"})
	if err != nil || !strings.HasPrefix(out.Secret, store.TokenPrefix) || out.Agent.Actor != "alice / ci" || len(out.Snippets) < 3 {
		t.Fatalf("agent add: %+v %v", out, err)
	}
	if !strings.Contains(out.Snippets[0].Text, "claude mcp add --transport http beacon") || !strings.Contains(out.Snippets[0].Text, out.Secret) {
		t.Errorf("snippets: %+v", out.Snippets)
	}
	// custom scopes from the command line, still within the owner's role
	cust, err := c.AddAgent(ctx, AgentRequest{Name: "ops", Owner: "alice", Preset: store.PresetCustom, Scopes: []string{"status:read", "config:write"}, TTL: "30d"})
	if err != nil || cust.Agent.Preset != store.PresetCustom || !strings.Contains(strings.Join(cust.Agent.Scopes, ","), "config:write") {
		t.Errorf("a custom agent: %+v %v", cust, err)
	}
	agents, err := c.Agents(ctx)
	if err != nil || len(agents) != 2 {
		t.Errorf("agent list: %+v %v", agents, err)
	}
	if err := c.RemoveAgent(ctx, "ops"); err != nil {
		t.Error(err)
	}
	if err := c.RemoveAgent(ctx, "ops"); err == nil {
		t.Error("removing twice")
	}

	// claiming a legacy token
	if _, err := c.CreateToken(ctx, TokenRequest{Name: "old-bot", Scopes: []string{"status:read", "alerts:write"}, TTL: "30d"}); err != nil {
		t.Fatal(err)
	}
	a, err := c.Claim(ctx, ClaimRequest{Token: "old-bot", Owner: "alice"})
	if err != nil || a.Actor != "alice / old-bot" || a.Preset != store.PresetCustom {
		t.Errorf("claim: %+v %v", a, err)
	}
	if _, err := c.Claim(ctx, ClaimRequest{Token: "old-bot", Owner: "alice"}); err == nil {
		t.Error("claimed twice")
	}

	// all of it audited under the command line's user
	who := inv.CreatedBy
	for _, action := range []string{"user_invite", "user_role", "user_disable", "user_reset_link", "agent_create", "agent_delete", "token_claim"} {
		found := false
		for _, en := range e.Store.AuditLog(100) {
			found = found || en.Actor == who && en.Action == action
		}
		if !found {
			t.Errorf("no %s by %s in the audit log", action, who)
		}
	}
}

// TestRevokeClosesSessions: revoking a token on the command line rechecks what is open on the channel.
func TestRevokeClosesSessions(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fm := &fakeMachines{}
	srv := httptest.NewServer(Handler(st, nil, fm))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	if _, err := c.CreateToken(context.Background(), TokenRequest{Name: "claude-code", Scopes: []string{"machines:ssh"}, TTL: "1d"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RevokeToken(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	if fm.rechecks != 1 {
		t.Errorf("rechecks: %d", fm.rechecks)
	}
}
