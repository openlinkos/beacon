package web

// LOCKED acceptance and security tests for managing machines and agents' custom scopes from the
// console. Written before the feature; the implementer must make them pass and must not edit, skip
// or delete them (checked by the coordinator's check-locked.sh).
//
// The console contract these tests pin:
//
//	POST /console/machines                    add a machine (machines:write): name, address, user, critical, note,
//	                                          connector_key, host_key; 201 with a one-time page that shows the CA line
//	POST /console/machines/{name}/edit        note, critical ("on" or absent), user; the host key is kept
//	POST /console/machines/{name}/replace     pin the host key again
//	POST /console/machines/{name}/connector   key: the key a machine behind NAT connects out with
//	POST /console/machines/{name}/remove      confirm: must equal the machine's name
//	POST /console/agents                      preset=custom with repeated scopes=… (owners only, people only)
//	POST /console/agents/{name}/settings      preset=custom with repeated scopes=… (owners only, people only)
//
// A refused form either answers 403 or redirects back with ?error=…; either way nothing changes.

import (
	"context"
	"html"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// lockSSHServer is an sshd that only offers its host key, enough for Beacon to pin it. It returns the
// address and the key's SHA-256 fingerprint.
func lockSSHServer(t *testing.T) (string, string) {
	t.Helper()
	signer := keySigner(t)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				_, _, _, _ = ssh.NewServerConn(c, cfg)
				_ = c.Close()
			}()
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}

func lockPubKey(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(keySigner(t).PublicKey())))
}

func lockRefused(a answer) bool {
	return a.code == http.StatusForbidden || a.code == http.StatusNotFound ||
		(a.code == http.StatusSeeOther && strings.Contains(a.header.Get("Location"), "error="))
}

func lockAuditCount(st *store.Store) int { return len(st.AuditLog(100000)) }

func lockAuditedDetail(st *store.Store, action, target string, parts ...string) bool {
	for _, a := range st.AuditLog(100000) {
		if a.Action != action || a.Target != target || a.Via != "console" {
			continue
		}
		ok := true
		for _, p := range parts {
			if !strings.Contains(a.Detail, p) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

var lockTokenRe = regexp.MustCompile(`<code>(lbt_[a-z0-9]+)</code>`)

// ---- Part A: machines

func TestLockConsoleMachineAdd(t *testing.T) {
	e := newEnv(t, i18n.EN)
	addr, fp := lockSSHServer(t)
	op := e.signedIn("operator", "status:read", "machines:write")

	page := op.get("/console/machines")
	if !strings.Contains(page.body, `action="/console/machines"`) {
		t.Fatalf("the machines' page offers an operator no form to add a machine")
	}
	// a token adds no critical machine: an owner, or beacon on the host, marks one
	if a := op.post("/console/machines", url.Values{"name": {"web-0"}, "address": {addr}, "critical": {"on"}}); !lockRefused(a) {
		t.Errorf("a token adds a critical machine: %d %q", a.code, a.header.Get("Location"))
	}
	if _, ok := e.st.Machine("web-0"); ok {
		t.Fatal("a refused critical machine was added")
	}
	a := op.post("/console/machines", url.Values{"name": {"web-1"}, "address": {addr}, "user": {"beacon"}, "note": {"web " + hostile}})
	if a.code != http.StatusCreated {
		t.Fatalf("adding a dialled machine: %d %s", a.code, a.body)
	}
	if !strings.Contains(html.UnescapeString(a.body), e.srv.SSH.CAPublicKey()) {
		t.Error("the one-time page lacks the TrustedUserCAKeys line")
	}
	if strings.Contains(a.body, hostile) {
		t.Error("the note is not escaped")
	}
	m, ok := e.st.Machine("web-1")
	if !ok || m.Address != addr || m.Critical || m.AddedBy != "operator" {
		t.Fatalf("the machine in the store: %+v %v", m, ok)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(m.HostKey))
	if err != nil || ssh.FingerprintSHA256(pk) != fp {
		t.Errorf("the pinned host key is not the machine's: %v", err)
	}
	if !audited(e.st, "operator", "machine_add") {
		t.Error("adding is not audited as the console's")
	}
	if !e.bot.told("operator", "web-1") {
		t.Errorf("the chat was not told: %v", e.bot.sent)
	}

	// a machine behind NAT
	a = op.post("/console/machines", url.Values{"name": {"nat-1"}, "address": {store.ConnectorAddress}, "connector_key": {lockPubKey(t)}})
	if a.code != http.StatusCreated {
		t.Fatalf("adding a connector machine: %d %s", a.code, a.body)
	}
	if n, ok := e.st.Machine("nat-1"); !ok || n.Address != store.ConnectorAddress || n.ConnectorKey == "" {
		t.Errorf("the connector machine: %+v %v", n, ok)
	}

	// adding one that exists, or with a bad name, changes nothing
	before := lockAuditCount(e.st)
	if a := op.post("/console/machines", url.Values{"name": {"web-1"}, "address": {addr}}); !lockRefused(a) {
		t.Errorf("adding web-1 twice: %d %q", a.code, a.header.Get("Location"))
	}
	if a := op.post("/console/machines", url.Values{"name": {"Bad Name"}, "address": {addr}}); !lockRefused(a) {
		t.Errorf("a bad name: %d %q", a.code, a.header.Get("Location"))
	}
	if lockAuditCount(e.st) != before {
		t.Error("a refused add was audited as a change")
	}
}

// lockMachineWrites are the machine forms, for the checks every one of them must pass.
func lockMachineWrites(addr string) map[string]url.Values {
	return map[string]url.Values{
		"/console/machines":                  {"name": {"web-9"}, "address": {addr}},
		"/console/machines/edge-1/edit":      {"note": {"changed"}, "user": {"ops"}},
		"/console/machines/edge-1/replace":   {},
		"/console/machines/edge-2/connector": {"key": {"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIc2hpaMVmZcVbUQM2LPRdl6obEbBmEd1+GQBUn5R4Re x"}},
		"/console/machines/edge-1/remove":    {"confirm": {"edge-1"}},
	}
}

func lockMachinesUnchanged(t *testing.T, e *env, what string) {
	t.Helper()
	if _, ok := e.st.Machine("web-9"); ok {
		t.Errorf("%s: a machine was added", what)
	}
	m, ok := e.st.Machine("edge-1")
	if !ok || m.Note != "the relay "+hostile || m.User != "beacon" {
		t.Errorf("%s: edge-1 changed: %+v %v", what, m, ok)
	}
	if n, _ := e.st.Machine("edge-2"); n.ConnectorKey != "" {
		t.Errorf("%s: edge-2's key changed", what)
	}
}

func TestLockConsoleMachineForbidden(t *testing.T) {
	e := newEnv(t, i18n.EN)
	addr, _ := lockSSHServer(t)
	audit := lockAuditCount(e.st)

	// without machines:write: no forms, and every post is refused
	for name, scopes := range map[string][]string{
		"viewer": {"status:read"},
		"oncall": {"status:read", "alerts:write", "machines:ssh"},
	} {
		b := e.signedIn(name, scopes...)
		list := b.get("/console/machines")
		one := b.get("/console/machines/edge-1")
		for _, form := range []string{`action="/console/machines"`, `/edit"`, `/replace"`, `/remove"`, `/connector"`} {
			if strings.Contains(list.body, form) || strings.Contains(one.body, form) {
				t.Errorf("%s is offered %s", name, form)
			}
		}
		for path, form := range lockMachineWrites(addr) {
			if a := b.post(path, clone(form)); a.code != http.StatusForbidden {
				t.Errorf("%s posts %s: %d", name, path, a.code)
			}
		}
	}
	// a person on call (an account, not a token) is refused too
	alice := e.owner()
	bob := e.invite(alice, "bob", "oncall")
	for path, form := range lockMachineWrites(addr) {
		if a := bob.post(path, clone(form)); a.code != http.StatusForbidden {
			t.Errorf("bob (on call) posts %s: %d", path, a.code)
		}
	}
	lockMachinesUnchanged(t, e, "without machines:write")

	// with machines:write but without the form's secret, cross-site, or signed out
	op := e.signedIn("operator", "status:read", "machines:write")
	for path, form := range lockMachineWrites(addr) {
		if a := op.do(http.MethodPost, path, clone(form)); a.code != http.StatusForbidden {
			t.Errorf("%s without the secret: %d", path, a.code)
		}
		f := clone(form)
		f.Set("csrf", op.csrf+"x")
		if a := op.do(http.MethodPost, path, f); a.code != http.StatusForbidden {
			t.Errorf("%s with a wrong secret: %d", path, a.code)
		}
		op.headers["Sec-Fetch-Site"] = "cross-site"
		if a := op.post(path, clone(form)); a.code != http.StatusForbidden {
			t.Errorf("%s cross-site: %d", path, a.code)
		}
		delete(op.headers, "Sec-Fetch-Site")
		if a := e.browser().do(http.MethodPost, path, clone(form)); a.code != http.StatusSeeOther || !strings.HasPrefix(a.header.Get("Location"), "/console/sign-in") {
			t.Errorf("%s signed out: %d", path, a.code)
		}
	}
	lockMachinesUnchanged(t, e, "forged forms")
	for _, a := range e.st.AuditLog(100000)[:lockAuditCount(e.st)-audit] {
		if strings.HasPrefix(a.Action, "machine_") {
			t.Errorf("a refused form was audited: %+v", a)
		}
	}
}

func TestLockConsoleMachineEdit(t *testing.T) {
	e := newEnv(t, i18n.EN)
	addr, fp := lockSSHServer(t)
	op := e.signedIn("operator", "status:read", "machines:write")
	if a := op.post("/console/machines", url.Values{"name": {"web-1"}, "address": {addr}, "note": {"first"}}); a.code != http.StatusCreated {
		t.Fatalf("add: %d %s", a.code, a.body)
	}
	before, _ := e.st.Machine("web-1")

	if page := op.get("/console/machines/web-1"); !strings.Contains(page.body, `action="/console/machines/web-1/edit"`) ||
		!strings.Contains(page.body, `action="/console/machines/web-1/replace"`) || !strings.Contains(page.body, `action="/console/machines/web-1/remove"`) {
		t.Fatalf("the machine's page lacks its forms")
	}
	// a token marks no machine critical and changes no account
	if a := op.post("/console/machines/web-1/edit", url.Values{"note": {"second"}, "critical": {"on"}, "user": {"ops"}}); !lockRefused(a) {
		t.Fatalf("a token marks critical and changes the account: %d %q", a.code, a.header.Get("Location"))
	}
	if m, _ := e.st.Machine("web-1"); m != before {
		t.Fatalf("a refused edit changed the machine: %+v", m)
	}
	a := op.post("/console/machines/web-1/edit", url.Values{"note": {"second"}, "user": {"beacon"}})
	if a.code != http.StatusSeeOther || strings.Contains(a.header.Get("Location"), "error=") {
		t.Fatalf("edit: %d %q", a.code, a.header.Get("Location"))
	}
	// an owner does both
	if a := e.owner().post("/console/machines/web-1/edit", url.Values{"note": {"second"}, "critical": {"on"}, "user": {"ops"}}); a.code != http.StatusSeeOther || strings.Contains(a.header.Get("Location"), "error=") {
		t.Fatalf("an owner's edit: %d %q", a.code, a.header.Get("Location"))
	}
	after, _ := e.st.Machine("web-1")
	if after.Note != "second" || !after.Critical || after.User != "ops" {
		t.Errorf("after the edit: %+v", after)
	}
	if after.HostKey != before.HostKey || after.Address != before.Address {
		t.Error("an edit changed the host key or the address")
	}
	if !audited(e.st, "operator", "machine_update") || !lockAuditedDetail(e.st, "machine_update", "web-1", "second") {
		t.Errorf("the edit is not audited with what changed: %+v", e.st.AuditLog(5))
	}
	if !e.bot.told("operator", "web-1") {
		t.Errorf("chat: %v", e.bot.sent)
	}
	// an account name that is not one is refused, and nothing changes
	if a := op.post("/console/machines/web-1/edit", url.Values{"note": {"third"}, "user": {"Root; rm -rf"}}); !lockRefused(a) {
		t.Errorf("a bad account: %d %q", a.code, a.header.Get("Location"))
	}
	if m, _ := e.st.Machine("web-1"); m.Note != "second" || m.User != "ops" {
		t.Errorf("a refused edit changed the machine: %+v", m)
	}
	// unknown machine
	if a := op.post("/console/machines/nope/edit", url.Values{"note": {"x"}}); !lockRefused(a) {
		t.Errorf("editing an unknown machine: %d", a.code)
	}

	// pinning again takes the machine's current key: a new server at the same name
	addr2, fp2 := lockSSHServer(t)
	if err := e.st.PutMachine(store.Machine{Name: "web-2", Address: addr2, User: "beacon", HostKey: before.HostKey, Added: e.now}); err != nil {
		t.Fatal(err)
	}
	if a := op.post("/console/machines/web-2/replace", nil); a.code != http.StatusSeeOther || strings.Contains(a.header.Get("Location"), "error=") {
		t.Fatalf("replace: %d %q", a.code, a.header.Get("Location"))
	}
	m2, _ := e.st.Machine("web-2")
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(m2.HostKey))
	if err != nil || ssh.FingerprintSHA256(pk) != fp2 || fp2 == fp {
		t.Errorf("replace did not pin the new key: %v", err)
	}
	found := false
	for _, x := range e.st.AuditLog(100000) {
		if x.Target == "web-2" && x.Via == "console" && strings.HasPrefix(x.Action, "machine_") && strings.Contains(x.Detail, fp2) {
			found = true
		}
	}
	if !found {
		t.Error("pinning again is not audited with the new fingerprint")
	}

	// a connector machine gets a new connect-out key
	if a := op.post("/console/machines/edge-2/connector", url.Values{"key": {lockPubKey(t)}}); a.code != http.StatusSeeOther || strings.Contains(a.header.Get("Location"), "error=") {
		t.Errorf("connector key: %d %q", a.code, a.header.Get("Location"))
	}
	if n, _ := e.st.Machine("edge-2"); n.ConnectorKey == "" {
		t.Error("the connector key was not set")
	}
}

func TestLockConsoleMachineRemove(t *testing.T) {
	e := newEnv(t, i18n.EN)
	op := e.signedIn("operator", "status:read", "machines:write")
	for _, wrong := range []string{"", "edge-2", "EDGE-1", "edge-1 "} {
		if a := op.post("/console/machines/edge-1/remove", url.Values{"confirm": {wrong}}); !lockRefused(a) {
			t.Errorf("confirm %q: %d %q", wrong, a.code, a.header.Get("Location"))
		}
	}
	if _, ok := e.st.Machine("edge-1"); !ok {
		t.Fatal("a wrong confirmation removed the machine")
	}
	for _, a := range e.st.AuditLog(100000) {
		if a.Action == "machine_remove" {
			t.Fatal("a refused removal was audited")
		}
	}
	// edge-1 is critical: a token does not remove it, an owner does
	if a := op.post("/console/machines/edge-1/remove", url.Values{"confirm": {"edge-1"}}); !lockRefused(a) {
		t.Fatalf("a token removes a critical machine: %d %q", a.code, a.header.Get("Location"))
	}
	if _, ok := e.st.Machine("edge-1"); !ok {
		t.Fatal("a refused removal removed the machine")
	}
	if a := e.owner().post("/console/machines/edge-1/remove", url.Values{"confirm": {"edge-1"}}); a.code != http.StatusSeeOther || strings.Contains(a.header.Get("Location"), "error=") {
		t.Fatalf("remove: %d %q", a.code, a.header.Get("Location"))
	}
	if _, ok := e.st.Machine("edge-1"); ok {
		t.Error("the machine outlives its removal")
	}
	if _, ok := e.st.Session(sessionID); !ok {
		t.Error("removing a machine lost its sessions")
	}
	if !audited(e.st, "alice", "machine_remove") || !e.bot.told("alice", "edge-1") {
		t.Error("removal not audited or not echoed")
	}
}

// ---- Part B: agents' custom scopes

// lockPeople is alice (owner), bob (on call) with his agent bot-b, carol (operator), and admin-bot: an
// agent of alice's made on the command line with every people's scope, accounts:write included.
type lockPeople struct {
	alice, bob, carol *browser
	adminBot          string // admin-bot's token
}

func lockSetup(t *testing.T, e *env) lockPeople {
	t.Helper()
	p := lockPeople{alice: e.owner()}
	p.bob = e.invite(p.alice, "bob", "oncall")
	p.carol = e.invite(p.alice, "carol", "operator")
	cli := bridge.Actor{Name: "cli:root", Via: "cli"}
	if _, err := e.b.CreateAgent(context.Background(), cli, bridge.AgentInput{Name: "bot-b", Owner: "bob", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	out, err := e.b.CreateAgent(context.Background(), cli, bridge.AgentInput{Name: "admin-bot", Owner: "alice", Preset: store.PresetCustom,
		Scopes: slices.Clone(store.PeopleScopes), TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	p.adminBot = out.Secret
	return p
}

func lockAgent(t *testing.T, e *env, name string) store.Agent {
	t.Helper()
	a, ok := e.st.Agent(name)
	if !ok {
		t.Fatalf("no agent %s", name)
	}
	return a
}

func customForm(name, owner string, scopes ...string) url.Values {
	f := url.Values{"name": {name}, "preset": {store.PresetCustom}, "expires": {"90d"}, "scopes": scopes}
	if owner != "" {
		f.Set("owner", owner)
	}
	return f
}

func TestLockConsoleAgentCustomScopes(t *testing.T) {
	e := newEnv(t, i18n.EN)
	p := lockSetup(t, e)

	// the owner is offered custom scopes; people below owner are not
	if page := p.alice.get("/console/agents"); !strings.Contains(page.body, `name="scopes"`) || !strings.Contains(page.body, `value="config:write"`) {
		t.Fatalf("the owner's agents page offers no custom scopes")
	}
	for name, b := range map[string]*browser{"bob": p.bob, "carol": p.carol} {
		if page := b.get("/console/agents"); strings.Contains(page.body, `name="scopes"`) {
			t.Errorf("%s is offered custom scopes", name)
		}
	}

	// the owner makes a custom agent with config:write, for herself
	a := p.alice.post("/console/agents", customForm("ops-bot", "alice", "status:read", "config:write"))
	if a.code != http.StatusCreated {
		t.Fatalf("custom agent: %d %q %s", a.code, a.header.Get("Location"), a.body)
	}
	m := lockTokenRe.FindStringSubmatch(a.body)
	if m == nil {
		t.Fatal("no token shown")
	}
	pr, ok := e.st.Authenticate(m[1])
	if !ok || !pr.Has("config:write") || !pr.Has("status:read") || pr.Has("machines:write") || pr.Has("accounts:write") {
		t.Errorf("the custom agent may do: %+v", pr.Scopes)
	}
	if ag := lockAgent(t, e, "ops-bot"); ag.Preset != store.PresetCustom || !slices.Equal(ag.Scopes, []string{"status:read", "config:write"}) && !slices.Equal(ag.Scopes, []string{"config:write", "status:read"}) {
		t.Errorf("the stored agent: %+v", ag)
	}
	if !lockAuditedDetail(e.st, "agent_create", "ops-bot", "custom") {
		t.Errorf("creating a custom agent is not audited: %+v", e.st.AuditLog(5))
	}
	if !e.bot.told("alice", "ops-bot", "via the console") {
		t.Errorf("chat: %v", e.bot.sent)
	}

	// the owner switches bob's agent to custom scopes within bob's role, and back
	if a := p.alice.post("/console/agents/bot-b/settings", url.Values{"preset": {store.PresetCustom}, "scopes": {"status:read", "alerts:write"}}); a.code != http.StatusSeeOther || strings.Contains(a.header.Get("Location"), "error=") {
		t.Fatalf("switch to custom: %d %q", a.code, a.header.Get("Location"))
	}
	if ag := lockAgent(t, e, "bot-b"); ag.Preset != store.PresetCustom || slices.Contains(ag.Scopes, "machines:ssh") || !slices.Contains(ag.Scopes, "alerts:write") {
		t.Errorf("bot-b after the switch: %+v", ag)
	}
	if !lockAuditedDetail(e.st, "agent_update", "bot-b", "custom", "alerts:write") {
		t.Errorf("the change of scopes is not audited with what changed: %+v", e.st.AuditLog(5))
	}
	if !e.bot.told("alice", "bot-b", "via the console") {
		t.Errorf("chat: %v", e.bot.sent)
	}
	if a := p.alice.post("/console/agents/bot-b/settings", url.Values{"preset": {"readonly"}}); a.code != http.StatusSeeOther || strings.Contains(a.header.Get("Location"), "error=") {
		t.Fatalf("back to a preset: %d %q", a.code, a.header.Get("Location"))
	}
	if ag := lockAgent(t, e, "bot-b"); ag.Preset != "readonly" || len(ag.Scopes) != 0 {
		t.Errorf("bot-b back on a preset: %+v", ag)
	}
}

func TestLockConsoleAgentCustomScopesRefused(t *testing.T) {
	e := newEnv(t, i18n.EN)
	p := lockSetup(t, e)
	agents := func() int { return len(e.st.Agents("")) }
	n := agents()
	botB := lockAgent(t, e, "bot-b")
	unchanged := func(what string) {
		t.Helper()
		if agents() != n {
			t.Errorf("%s: an agent was made", what)
		}
		if ag := lockAgent(t, e, "bot-b"); ag.Preset != botB.Preset || !slices.Equal(ag.Scopes, botB.Scopes) {
			t.Errorf("%s: bot-b changed: %+v", what, ag)
		}
	}

	// people below owner may not make or set custom scopes, not even within their own role
	for name, b := range map[string]*browser{"bob": p.bob, "carol": p.carol} {
		if a := b.post("/console/agents", customForm("x-"+name, "", "status:read")); !lockRefused(a) {
			t.Errorf("%s makes a custom agent: %d %q", name, a.code, a.header.Get("Location"))
		}
	}
	if a := p.bob.post("/console/agents/bot-b/settings", url.Values{"preset": {store.PresetCustom}, "scopes": {"status:read"}}); !lockRefused(a) {
		t.Errorf("bob sets custom scopes on his own agent: %d %q", a.code, a.header.Get("Location"))
	}
	unchanged("people below owner")

	// not beyond the agent's owner's role, not an unknown scope, not a machine's scope, not none
	for what, form := range map[string]url.Values{
		"beyond bob's role":  customForm("x-1", "bob", "status:read", "config:write"),
		"beyond bob's role2": customForm("x-2", "bob", "machines:write"),
		"unknown scope":      customForm("x-3", "alice", "status:read", "root:all"),
		"machine's scope":    customForm("x-4", "alice", "probe:write"),
		"rules scope":        customForm("x-5", "alice", "alerts:send"),
		"no scope":           customForm("x-6", "alice"),
	} {
		if a := p.alice.post("/console/agents", form); !lockRefused(a) {
			t.Errorf("%s: %d %q", what, a.code, a.header.Get("Location"))
		}
	}
	for what, scopes := range map[string][]string{
		"beyond bob's role": {"status:read", "config:write"},
		"accounts:write":    {"accounts:write"},
		"unknown":           {"everything"},
		"machine's":         {"host:write"},
	} {
		if a := p.alice.post("/console/agents/bot-b/settings", url.Values{"preset": {store.PresetCustom}, "scopes": scopes}); !lockRefused(a) {
			t.Errorf("settings %s: %d %q", what, a.code, a.header.Get("Location"))
		}
	}
	unchanged("beyond the role")

	// an agent never widens scopes, its own or anyone's, even one holding accounts:write
	ab := e.browser()
	if a := ab.signIn(p.adminBot, "/console"); a.code != http.StatusSeeOther {
		t.Fatalf("admin-bot signs in: %d", a.code)
	}
	ab.get("/console")
	if a := ab.post("/console/agents", customForm("x-7", "alice", "status:read", "config:write")); !lockRefused(a) {
		t.Errorf("an agent makes a custom agent: %d %q", a.code, a.header.Get("Location"))
	}
	if a := ab.post("/console/agents/bot-b/settings", url.Values{"preset": {store.PresetCustom}, "scopes": {"status:read", "alerts:write", "machines:ssh", "sessions:read"}}); !lockRefused(a) {
		t.Errorf("an agent sets another's custom scopes: %d %q", a.code, a.header.Get("Location"))
	}
	ops := lockAgent(t, e, "admin-bot")
	if a := ab.post("/console/agents/admin-bot/settings", url.Values{"preset": {store.PresetCustom}, "scopes": {"status:read"}}); !lockRefused(a) {
		t.Errorf("an agent changes its own scopes: %d %q", a.code, a.header.Get("Location"))
	}
	if ag := lockAgent(t, e, "admin-bot"); !slices.Equal(ag.Scopes, ops.Scopes) {
		t.Errorf("admin-bot changed itself: %+v", ag)
	}
	unchanged("an agent")

	// a legacy token with accounts:write but without config:write grants no config:write
	legacyAdmin := e.token("legacy-admin", "status:read", "accounts:write")
	lb := e.browser()
	if a := lb.signIn(legacyAdmin, "/console"); a.code != http.StatusSeeOther {
		t.Fatalf("legacy-admin signs in: %d", a.code)
	}
	lb.get("/console")
	if a := lb.post("/console/agents", customForm("x-8", "alice", "status:read", "config:write")); !lockRefused(a) {
		t.Errorf("granting a scope the granter does not hold: %d %q", a.code, a.header.Get("Location"))
	}
	unchanged("beyond the granter")

	// forged forms: without the secret, cross-site, signed out
	f := url.Values{"preset": {store.PresetCustom}, "scopes": {"status:read", "alerts:write"}}
	for name, try := range map[string]func() answer{
		"no secret": func() answer { return p.alice.do(http.MethodPost, "/console/agents/bot-b/settings", clone(f)) },
		"wrong secret": func() answer {
			g := clone(f)
			g.Set("csrf", p.alice.csrf+"x")
			return p.alice.do(http.MethodPost, "/console/agents/bot-b/settings", g)
		},
		"cross-site": func() answer {
			p.alice.headers["Sec-Fetch-Site"] = "cross-site"
			defer delete(p.alice.headers, "Sec-Fetch-Site")
			return p.alice.post("/console/agents/bot-b/settings", clone(f))
		},
		"create, no secret": func() answer {
			return p.alice.do(http.MethodPost, "/console/agents", customForm("x-9", "alice", "status:read"))
		},
	} {
		if a := try(); a.code != http.StatusForbidden {
			t.Errorf("%s: %d", name, a.code)
		}
	}
	if a := e.browser().do(http.MethodPost, "/console/agents/bot-b/settings", clone(f)); a.code != http.StatusSeeOther || !strings.HasPrefix(a.header.Get("Location"), "/console/sign-in") {
		t.Errorf("signed out: %d", a.code)
	}
	unchanged("forged forms")
	for _, a := range e.st.AuditLog(100000) {
		if (a.Action == "agent_update" || a.Action == "agent_create") && a.Via == "console" {
			t.Errorf("a refused change was audited: %+v", a)
		}
	}
}

func TestLockConsoleOwnerDemotionLimitsCustomAgent(t *testing.T) {
	e := newEnv(t, i18n.EN)
	p := lockSetup(t, e)
	e.invite(p.alice, "dave", "owner")
	a := p.alice.post("/console/agents", customForm("dave-bot", "dave", "status:read", "config:write"))
	if a.code != http.StatusCreated {
		t.Fatalf("custom agent for dave: %d %q", a.code, a.header.Get("Location"))
	}
	m := lockTokenRe.FindStringSubmatch(a.body)
	if m == nil {
		t.Fatal("no token")
	}
	if pr, _ := e.st.Authenticate(m[1]); !pr.Has("config:write") {
		t.Fatal("dave's agent lacks config:write")
	}
	if a := p.alice.post("/console/people/dave/role", url.Values{"role": {"operator"}}); a.code != http.StatusSeeOther {
		t.Fatalf("demote: %d", a.code)
	}
	if pr, ok := e.st.Authenticate(m[1]); !ok || pr.Has("config:write") {
		t.Error("demoting the owner did not take config:write from the agent's token at once")
	}
}

func TestLockConsoleLegacyTokenNotWidened(t *testing.T) {
	e := newEnv(t, i18n.EN)
	p := lockSetup(t, e)
	scopes := []string{"status:read", "alerts:write", "machines:ssh", "sessions:read"}
	secret := e.token("grokbot-oncall", scopes...)

	// a legacy token is no agent: its settings cannot be posted
	if a := p.alice.post("/console/agents/grokbot-oncall/settings", url.Values{"preset": {store.PresetCustom}, "scopes": {"config:write"}}); !lockRefused(a) {
		t.Errorf("settings on a legacy token: %d %q", a.code, a.header.Get("Location"))
	}
	if pr, _ := e.st.Authenticate(secret); pr.Has("config:write") {
		t.Fatal("the legacy token was widened")
	}
	// claiming keeps exactly its scopes, whatever else the form carries
	if a := p.alice.post("/console/tokens/grokbot-oncall/claim", url.Values{"owner": {"alice"}, "agent": {"grokbot-oncall"}, "preset": {store.PresetCustom},
		"scopes": {"config:write", "accounts:write"}}); a.code != http.StatusSeeOther {
		t.Fatalf("claim: %d %q", a.code, a.header.Get("Location"))
	}
	pr, ok := e.st.Authenticate(secret)
	if !ok || pr.Has("config:write") || pr.Has("accounts:write") || pr.Has("machines:write") {
		t.Errorf("the claimed token was widened: %+v", pr.Scopes)
	}
	for _, sc := range scopes {
		if !pr.Has(sc) {
			t.Errorf("the claimed token lost %s", sc)
		}
	}
	// signed in with the legacy token itself, it cannot widen itself
	tb := e.browser()
	if a := tb.signIn(secret, "/console"); a.code != http.StatusSeeOther {
		t.Fatalf("sign in: %d", a.code)
	}
	tb.get("/console")
	if a := tb.post("/console/agents/grokbot-oncall/settings", url.Values{"preset": {store.PresetCustom}, "scopes": {"status:read", "config:write"}}); !lockRefused(a) {
		t.Errorf("the token widens itself: %d %q", a.code, a.header.Get("Location"))
	}
	if pr, _ := e.st.Authenticate(secret); pr.Has("config:write") {
		t.Error("the token widened itself")
	}
}
