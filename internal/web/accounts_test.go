package web

import (
	"html"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// TestMain makes password hashing cheap for the tests: the parameters are recorded in each hash, so the
// code under test is the same.
func TestMain(m *testing.M) {
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

const password = "correct horse battery"

var linkRe = regexp.MustCompile(`<code>(https://beacon\.example\.com/console/[a-z]+\?code=[^<]+)</code>`)

// shownLink is the one-time link a page shows, as a path with its query.
func shownLink(t *testing.T, body string) string {
	t.Helper()
	m := linkRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no link on the page: %s", body)
	}
	return strings.TrimPrefix(html.UnescapeString(m[1]), publicURL)
}

// fill sends a one-time link's form (setup, join, reset) with its own secret.
func (b *browser) fill(path string, fields url.Values) answer {
	b.e.t.Helper()
	page := b.get(path)
	m := formRe.FindStringSubmatch(page.body)
	if page.code != http.StatusOK || m == nil {
		b.e.t.Fatalf("%s: %d %s", path, page.code, page.body)
	}
	u, _ := url.Parse(path)
	form := url.Values{"code": {u.Query().Get("code")}, "form": {m[1]}}
	for k, v := range fields {
		form[k] = v
	}
	return b.do(http.MethodPost, u.Path, form)
}

// signInAs signs in with a username and a password.
func (b *browser) signInAs(username, pw string) answer {
	b.e.t.Helper()
	page := b.get("/console/sign-in")
	m := formRe.FindStringSubmatch(page.body)
	if m == nil {
		b.e.t.Fatalf("sign-in page: %s", page.body)
	}
	return b.do(http.MethodPost, "/console/sign-in", url.Values{"username": {username}, "password": {pw}, "form": {m[1]}})
}

// owner sets Beacon up and returns alice's browser, signed in.
func (e *env) owner() *browser {
	e.t.Helper()
	code, _, err := e.st.CreateSetupLink()
	if err != nil {
		e.t.Fatal(err)
	}
	b := e.browser()
	if a := b.fill("/console/setup?code="+url.QueryEscape(code), url.Values{"username": {"alice"}, "display_name": {"Alice Zhang"}, "password": {password}, "again": {password}}); a.code != http.StatusSeeOther {
		e.t.Fatalf("setup: %d %s", a.code, a.body)
	}
	b.get("/console")
	return b
}

// invite has alice invite someone with role, and that person join; it returns their browser.
func (e *env) invite(alice *browser, username, role string) *browser {
	e.t.Helper()
	a := alice.post("/console/people/invite", url.Values{"role": {role}, "note": {"for " + username}})
	if a.code != http.StatusCreated {
		e.t.Fatalf("invite: %d %s", a.code, a.body)
	}
	b := e.browser()
	if a := b.fill(shownLink(e.t, a.body), url.Values{"username": {username}, "password": {password}, "again": {password}}); a.code != http.StatusSeeOther {
		e.t.Fatalf("join as %s: %d %s", username, a.code, a.body)
	}
	b.get("/console")
	alice.get("/console")
	return b
}

func TestSetupAndSignInWithPassword(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.browser()

	// nobody has an account: the sign-in page says where the setup link is, and offers the token form
	page := b.get("/console/sign-in")
	if !strings.Contains(page.body, "Nobody has an account yet") || !strings.Contains(page.body, `name="token"`) {
		t.Errorf("sign-in before setup: %s", page.body)
	}
	// a setup page with a wrong code says so, with no form
	if a := b.get("/console/setup?code=nope"); a.code != http.StatusOK || !strings.Contains(a.body, "This link does not work") || strings.Contains(a.body, `name="password"`) {
		t.Errorf("a wrong setup code: %d", a.code)
	}
	code, _, _ := e.st.CreateSetupLink()
	path := "/console/setup?code=" + url.QueryEscape(code)
	form := url.Values{"username": {"alice"}, "display_name": {"Alice <Zhang>"}, "password": {password}, "again": {password}}
	// without the form's own secret (from another site): refused
	if a := b.do(http.MethodPost, "/console/setup", url.Values{"code": {code}, "username": {"alice"}, "password": {password}, "again": {password}}); a.code != http.StatusForbidden {
		t.Errorf("setup without the form's secret: %d", a.code)
	}
	bad := url.Values{"username": {"alice"}, "password": {password}, "again": {"something else"}}
	if a := b.fill(path, bad); a.code != http.StatusBadRequest || !strings.Contains(a.body, "The passwords differ.") {
		t.Errorf("differing passwords: %d", a.code)
	}
	weak := url.Values{"username": {"alice"}, "password": {"password123"}, "again": {"password123"}}
	if a := b.fill(path, weak); a.code != http.StatusBadRequest || !strings.Contains(a.body, "one of the commonest") {
		t.Errorf("a common password: %d", a.code)
	}
	a := b.fill(path, form)
	if a.code != http.StatusSeeOther || a.header.Get("Location") != "/console" || b.cookies[sessionCookie] == nil {
		t.Fatalf("setup: %d %s", a.code, a.body)
	}
	home := b.get("/console")
	if !strings.Contains(home.body, "Alice &lt;Zhang&gt;") || !strings.Contains(home.body, "alice · Owner") || !strings.Contains(home.body, `href="/console/people"`) {
		t.Errorf("signed in as the owner: %s", home.body)
	}
	if !e.bot.told("alice set Beacon up and is its first owner") || e.st.AuditLog(5)[1].Action != "setup" {
		t.Errorf("setup not echoed or audited: %v %+v", e.bot.sent, e.st.AuditLog(5))
	}
	// the setup link is spent
	if a := e.browser().get(path); !strings.Contains(a.body, "Beacon is set up already") || strings.Contains(a.body, `name="password"`) {
		t.Errorf("the setup link twice: %d", a.code)
	}

	// signing out and in again with the password
	b.post("/console/sign-out", nil)
	if a := b.signInAs("alice", "wrong password"); a.code != http.StatusUnauthorized || !strings.Contains(a.body, "Wrong username or password.") || b.cookies[sessionCookie] != nil {
		t.Errorf("a wrong password: %d", a.code)
	}
	if a := b.signInAs("nobody", password); a.code != http.StatusUnauthorized || !strings.Contains(a.body, "Wrong username or password.") {
		t.Errorf("an unknown user: %d", a.code)
	}
	if a := b.signInAs("Alice", password); a.code != http.StatusSeeOther {
		t.Errorf("signing in: %d %s", a.code, a.body)
	}
	c := b.cookies[sessionCookie]
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || strings.Contains(c.Value, password) {
		t.Fatalf("the session cookie: %+v", c)
	}
	if e.st.AuditLog(1)[0].Action != "console_sign_in" || e.st.AuditLog(1)[0].Actor != "alice" {
		t.Errorf("the sign-in is not audited as alice: %+v", e.st.AuditLog(1))
	}
	// the token form is still there, secondary
	if a := e.browser().get("/console/sign-in?with=token"); !strings.Contains(a.body, `name="token"`) {
		t.Error("no token form")
	}

	// a username's wrong passwords are limited, from anywhere: then even the right one waits
	m := e.browser()
	for range 10 {
		m.signInAs("alice", "not her password")
	}
	if a := m.signInAs("alice", password); a.code != http.StatusTooManyRequests {
		t.Errorf("after ten wrong passwords: %d", a.code)
	}
}

// TestOneTimeLinks checks what a setup, invitation or reset link survives — the page loading twice,
// a form that fails — and what spends it: one successful submission. A link that merely expired says
// so, not "used already".
func TestOneTimeLinks(t *testing.T) {
	e := newEnv(t, i18n.EN)

	// a setup link whose time is up says it expired
	old, _, err := e.st.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(31 * time.Minute)
	if a := e.browser().get("/console/setup?code=" + url.QueryEscape(old)); !strings.Contains(a.body, "This link has expired") || strings.Contains(a.body, "used already") {
		t.Errorf("an expired setup link: %s", a.body)
	}
	e.now = e.now.Add(-31 * time.Minute)

	// a setup link survives two loads and forms that fail; the form that succeeds spends it
	code, _, err := e.st.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	path := "/console/setup?code=" + url.QueryEscape(code)
	b := e.browser()
	if p1, p2 := b.get(path), b.get(path); p1.code != http.StatusOK || p2.code != http.StatusOK || !strings.Contains(p2.body, `name="password"`) {
		t.Fatalf("the setup page after two loads: %d %d %s", p1.code, p2.code, p2.body)
	}
	if a := b.fill(path, url.Values{"username": {"alice"}, "password": {password}, "again": {"differs"}}); a.code != http.StatusBadRequest {
		t.Fatalf("differing passwords: %d %s", a.code, a.body)
	}
	if a := b.fill(path, url.Values{"username": {"alice"}, "password": {"password123"}, "again": {"password123"}}); a.code != http.StatusBadRequest {
		t.Fatalf("a common password: %d %s", a.code, a.body)
	}
	if a := b.fill(path, url.Values{"username": {"bad name"}, "password": {password}, "again": {password}}); a.code != http.StatusBadRequest {
		t.Fatalf("a bad username: %d %s", a.code, a.body)
	}
	if p := b.get(path); !strings.Contains(p.body, `name="password"`) {
		t.Fatalf("the setup link spent by failures: %s", p.body)
	}
	if a := b.fill(path, url.Values{"username": {"alice"}, "display_name": {"Alice"}, "password": {password}, "again": {password}}); a.code != http.StatusSeeOther {
		t.Fatalf("setup: %d %s", a.code, a.body)
	}
	b.get("/console")

	// an invitation: two loads and a taken username leave it whole; one join spends it
	a := b.post("/console/people/invite", url.Values{"role": {"oncall"}, "note": {"links"}})
	if a.code != http.StatusCreated {
		t.Fatalf("invite: %d %s", a.code, a.body)
	}
	join := shownLink(t, a.body)
	c := e.browser()
	if p1, p2 := c.get(join), c.get(join); p1.code != http.StatusOK || p2.code != http.StatusOK || !strings.Contains(p2.body, `name="password"`) {
		t.Fatalf("the join page after two loads: %d %d %s", p1.code, p2.code, p2.body)
	}
	if a := c.fill(join, url.Values{"username": {"alice"}, "password": {password}, "again": {password}}); a.code != http.StatusBadRequest {
		t.Fatalf("a taken username through an invitation: %d %s", a.code, a.body)
	}
	a = b.post("/console/people/invite", url.Values{"role": {"viewer"}, "note": {"expired"}})
	if a.code != http.StatusCreated {
		t.Fatalf("second invite: %d %s", a.code, a.body)
	}
	stale := shownLink(t, a.body)
	e.now = e.now.Add(25 * time.Hour)
	if p := e.browser().get(stale); !strings.Contains(p.body, "This link has expired") || strings.Contains(p.body, "used already") {
		t.Errorf("an expired invitation: %s", p.body)
	}
	e.now = e.now.Add(-25 * time.Hour)
	if a := c.fill(join, url.Values{"username": {"bob"}, "password": {password}, "again": {password}}); a.code != http.StatusSeeOther {
		t.Fatalf("join: %d %s", a.code, a.body)
	}
	if p := e.browser().get(join); !strings.Contains(p.body, "used already, or it is wrong") {
		t.Errorf("the invitation after use: %s", p.body)
	}

	// a reset link: two loads and a common password leave it whole; one reset spends it
	r := b.post("/console/people/bob/reset", nil)
	if r.code != http.StatusCreated {
		t.Fatalf("reset link: %d %s", r.code, r.body)
	}
	reset := shownLink(t, r.body)
	d := e.browser()
	if p1, p2 := d.get(reset), d.get(reset); p1.code != http.StatusOK || p2.code != http.StatusOK || !strings.Contains(p2.body, `name="password"`) {
		t.Fatalf("the reset page after two loads: %d %d %s", p1.code, p2.code, p2.body)
	}
	if a := d.fill(reset, url.Values{"password": {"password123"}, "again": {"password123"}}); a.code != http.StatusBadRequest {
		t.Fatalf("a common password through a reset: %d %s", a.code, a.body)
	}
	r = b.post("/console/people/alice/reset", nil)
	if r.code != http.StatusCreated {
		t.Fatalf("second reset link: %d %s", r.code, r.body)
	}
	stale = shownLink(t, r.body)
	e.now = e.now.Add(31 * time.Minute)
	if p := e.browser().get(stale); !strings.Contains(p.body, "This link has expired") || strings.Contains(p.body, "used already") {
		t.Errorf("an expired reset link: %s", p.body)
	}
	e.now = e.now.Add(-31 * time.Minute)
	if a := d.fill(reset, url.Values{"password": {"a whole new password"}, "again": {"a whole new password"}}); a.code != http.StatusSeeOther {
		t.Fatalf("reset: %d %s", a.code, a.body)
	}
	if p := e.browser().get(reset); !strings.Contains(p.body, "used already, or it is wrong") {
		t.Errorf("the reset link after use: %s", p.body)
	}
}

func TestPeopleAndRoles(t *testing.T) {
	e := newEnv(t, i18n.EN)
	alice := e.owner()
	bob := e.invite(alice, "bob", "oncall")

	if !e.bot.told("alice invited someone to join as On call via the console") || !e.bot.told("bob joined Beacon as On call, invited by alice") {
		t.Errorf("chat: %v", e.bot.sent)
	}
	for _, s := range e.bot.sent {
		if strings.Contains(s, "code=") {
			t.Errorf("a link reached the chat: %s", s)
		}
	}
	people := alice.get("/console/people")
	checkPage(t, "/console/people", people.body)
	if !strings.Contains(people.body, "bob") || !strings.Contains(people.body, "On call") {
		t.Errorf("people: %s", people.body)
	}
	// bob is on call: not an owner's pages or forms
	if a := bob.get("/console/people"); a.code != http.StatusForbidden || !strings.Contains(a.body, "Not with your role") {
		t.Errorf("bob on the people page: %d", a.code)
	}
	if a := bob.post("/console/people/invite", url.Values{"role": {"owner"}}); a.code != http.StatusForbidden {
		t.Errorf("bob invites: %d", a.code)
	}
	if strings.Contains(bob.get("/console").body, `href="/console/people"`) {
		t.Error("bob's nav has the people page")
	}
	// on call acknowledges alerts; a viewer does not
	if a := bob.post("/console/alerts/"+e.id+"/notes", url.Values{"text": {"looking"}}); a.code != http.StatusSeeOther {
		t.Errorf("bob's note: %d %s", a.code, a.body)
	}
	if n := e.st.AuditLog(1)[0]; n.Actor != "bob" || n.Via != "console" {
		t.Errorf("the note's actor: %+v", n)
	}
	// CSRF: a form without the session's secret does nothing
	if a := alice.post("/console/people/bob/role", url.Values{"role": {"viewer"}, "csrf": {"wrong"}}); a.code != http.StatusForbidden {
		t.Errorf("a role change without the CSRF secret: %d", a.code)
	}
	if a := alice.post("/console/people/bob/role", url.Values{"role": {"viewer"}}); a.code != http.StatusSeeOther {
		t.Fatalf("role: %d %s", a.code, a.body)
	}
	if a := bob.post("/console/alerts/"+e.id+"/notes", url.Values{"text": {"again"}}); a.code != http.StatusForbidden {
		t.Errorf("a viewer's note: %d", a.code)
	}
	if !e.bot.told("alice changed the role of bob to Viewer via the console") {
		t.Errorf("chat: %v", e.bot.sent)
	}
	// the last owner stays, and nobody disables themself
	if a := alice.post("/console/people/alice/role", url.Values{"role": {"viewer"}}); a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "last_owner") {
		t.Errorf("demoting the last owner: %d %q", a.code, a.header.Get("Location"))
	}
	if a := alice.post("/console/people/alice/disable", nil); !strings.Contains(a.header.Get("Location"), "self") {
		t.Errorf("disabling herself: %q", a.header.Get("Location"))
	}
	// disabling ends bob's sessions at once
	if a := alice.post("/console/people/bob/disable", nil); a.code != http.StatusSeeOther {
		t.Fatalf("disable: %d", a.code)
	}
	if a := bob.get("/console"); a.code != http.StatusSeeOther {
		t.Errorf("a disabled person's session: %d", a.code)
	}
	if a := bob.signInAs("bob", password); a.code != http.StatusForbidden || !strings.Contains(a.body, "This account is disabled") {
		t.Errorf("a disabled person signs in: %d", a.code)
	}
	alice.post("/console/people/bob/enable", nil)

	// a reset link: shown once to alice, chosen by bob, every sign-in of his ended
	bob.signInAs("bob", password)
	a := alice.post("/console/people/bob/reset", nil)
	if a.code != http.StatusCreated {
		t.Fatalf("reset link: %d %s", a.code, a.body)
	}
	other := e.browser()
	if r := other.fill(shownLink(t, a.body), url.Values{"password": {"a whole new password"}, "again": {"a whole new password"}}); r.code != http.StatusSeeOther {
		t.Fatalf("reset: %d %s", r.code, r.body)
	}
	if r := bob.get("/console"); r.code != http.StatusSeeOther {
		t.Error("bob's old session outlived the reset")
	}
	if r := bob.signInAs("bob", "a whole new password"); r.code != http.StatusSeeOther {
		t.Errorf("the new password: %d", r.code)
	}

	// changing one's own password needs the current one, and ends the other sessions
	second := e.browser()
	second.signInAs("alice", password)
	if r := alice.post("/console/account/password", url.Values{"current": {"wrong"}, "password": {"alice new password"}, "again": {"alice new password"}}); !strings.Contains(r.header.Get("Location"), "password_wrong") {
		t.Errorf("without the current password: %q", r.header.Get("Location"))
	}
	r := alice.post("/console/account/password", url.Values{"current": {password}, "password": {"alice new password"}, "again": {"alice new password"}})
	if !strings.Contains(r.header.Get("Location"), "done=password") {
		t.Errorf("change: %q", r.header.Get("Location"))
	}
	if r := alice.get(r.header.Get("Location")); r.code != http.StatusOK || !strings.Contains(r.body, "Password changed") {
		t.Errorf("the session that changed it: %d", r.code)
	}
	if r := second.get("/console"); r.code != http.StatusSeeOther {
		t.Error("another session outlived the password change")
	}
	// invitations can be withdrawn
	alice.post("/console/people/invite", url.Values{"role": {"viewer"}})
	people = alice.get("/console/people")
	m := regexp.MustCompile(`action="(/console/people/invitations/[^"]+/withdraw)"`).FindStringSubmatch(people.body)
	if m == nil {
		t.Fatalf("no invitation to withdraw: %s", people.body)
	}
	if r := alice.post(html.UnescapeString(m[1]), nil); !strings.Contains(r.header.Get("Location"), "withdrawn") {
		t.Errorf("withdraw: %q", r.header.Get("Location"))
	}
}

func TestAgentsOnTheConsole(t *testing.T) {
	e := newEnv(t, i18n.EN)
	alice := e.owner()
	bob := e.invite(alice, "bob", "oncall")

	page := bob.get("/console/agents")
	checkPage(t, "/console/agents", page.body)
	if strings.Contains(page.body, `<option value="operator"`) || !strings.Contains(page.body, `<option value="oncall" selected`) {
		t.Errorf("bob may choose more than his role: %s", page.body)
	}
	// a preset above bob's role is refused
	if a := bob.post("/console/agents", url.Values{"name": {"ci"}, "preset": {"operator"}, "expires": {"365d"}}); !strings.Contains(a.header.Get("Location"), "error=bad_agent") {
		t.Errorf("a preset above the role: %q", a.header.Get("Location"))
	}
	a := bob.post("/console/agents", url.Values{"name": {"ci-bot"}, "preset": {"oncall"}, "machines": {"edge-1"}, "expires": {"90d"}})
	if a.code != http.StatusCreated {
		t.Fatalf("new agent: %d %s", a.code, a.body)
	}
	secret := regexp.MustCompile(`<code>(lbt_[a-z0-9]+)</code>`).FindStringSubmatch(a.body)
	if secret == nil || !strings.Contains(a.body, "claude mcp add --transport http beacon https://beacon.example.com/mcp --header &#34;Authorization: Bearer "+secret[1]) ||
		!strings.Contains(a.body, "codex mcp add beacon --url https://beacon.example.com/mcp --bearer-token-env-var BEACON_TOKEN") {
		t.Fatalf("the token and its snippets: %s", a.body)
	}
	p, ok := e.st.Authenticate(secret[1])
	if !ok || p.Name() != "bob / ci-bot" || !p.Reaches("edge-1") || p.Reaches("edge-2") || p.Has("machines:write") {
		t.Errorf("the agent's token: %+v %v", p, ok)
	}
	if !e.bot.told("bob made agent ci-bot for bob (On call) via the console") || !e.bot.told("bob gave agent ci-bot a token") {
		t.Errorf("chat: %v", e.bot.sent)
	}
	for _, s := range e.bot.sent {
		if strings.Contains(s, secret[1]) {
			t.Error("the token reached the chat")
		}
	}
	// the agent's page: settings, a key, revoking and deleting
	ap := bob.get("/console/agents/ci-bot")
	checkPage(t, "/console/agents/ci-bot", ap.body)
	if !strings.Contains(ap.body, "bob / ci-bot") || !strings.Contains(ap.body, "edge-1") {
		t.Errorf("agent page: %s", ap.body)
	}
	key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(keySigner(t).PublicKey()))) + " laptop"
	if a := bob.post("/console/agents/ci-bot/keys", url.Values{"key": {key}}); !strings.Contains(a.header.Get("Location"), "key_added") {
		t.Errorf("add key: %q", a.header.Get("Location"))
	}
	if a := bob.post("/console/agents/ci-bot/keys", url.Values{"key": {"not a key"}}); !strings.Contains(a.header.Get("Location"), "bad_key") {
		t.Errorf("a bad key: %q", a.header.Get("Location"))
	}
	ag, _ := e.st.Agent("ci-bot")
	if len(ag.SSHKeys) != 1 || ag.SSHKeys[0].Comment != "laptop" {
		t.Errorf("keys: %+v", ag.SSHKeys)
	}
	if a := bob.post("/console/agents/ci-bot/settings", url.Values{"preset": {"readonly"}, "machines": {""}}); !strings.Contains(a.header.Get("Location"), "done=saved") {
		t.Errorf("settings: %q", a.header.Get("Location"))
	}
	if p, _ := e.st.Authenticate(secret[1]); p.Has("alerts:write") || !p.Reaches("edge-2") && p.Has("machines:ssh") {
		t.Errorf("after the settings: %+v", p)
	}
	// carol, another person, may not touch bob's agent; alice, an owner, sees it
	carol := e.invite(alice, "carol", "operator")
	if a := carol.get("/console/agents/ci-bot"); a.code != http.StatusNotFound {
		t.Errorf("carol on bob's agent: %d", a.code)
	}
	if a := carol.post("/console/agents/ci-bot/delete", nil); !strings.Contains(a.header.Get("Location"), "forbidden") {
		t.Errorf("carol deletes bob's agent: %q", a.header.Get("Location"))
	}
	if a := alice.get("/console/agents"); !strings.Contains(a.body, "ci-bot") {
		t.Error("the owner does not see bob's agent")
	}
	if a := bob.post("/console/agents/ci-bot/tokens/ci-bot/revoke", nil); !strings.Contains(a.header.Get("Location"), "done=revoked") {
		t.Errorf("revoke: %q", a.header.Get("Location"))
	}
	if _, ok := e.st.Authenticate(secret[1]); ok {
		t.Error("a revoked token works")
	}
	if a := bob.post("/console/agents/ci-bot/delete", nil); !strings.Contains(a.header.Get("Location"), "agent_deleted") {
		t.Errorf("delete: %q", a.header.Get("Location"))
	}
	if _, ok := e.st.Agent("ci-bot"); ok {
		t.Error("the agent outlives deleting it")
	}
	if !e.bot.told("bob deleted agent ci-bot of bob via the console") {
		t.Errorf("chat: %v", e.bot.sent)
	}
	// the old hooks page is the hooks page now
	if a := bob.get("/console/hooks"); a.code != http.StatusOK || !strings.Contains(a.body, "hooks.example.com") {
		t.Errorf("hooks: %d", a.code)
	}
}

func TestLegacyTokens(t *testing.T) {
	e := newEnv(t, i18n.EN)
	// a token from before accounts keeps signing in, and its actions keep its name
	legacy := e.token("claude-code", "status:read", "alerts:write", "machines:ssh")
	tb := e.browser()
	if a := tb.signIn(legacy, "/console"); a.code != http.StatusSeeOther {
		t.Fatalf("a legacy token signs in: %d", a.code)
	}
	tb.get("/console")
	tb.post("/console/alerts/"+e.id+"/notes", url.Values{"text": {"legacy"}})
	if n := e.st.AuditLog(1)[0]; n.Actor != "claude-code" {
		t.Errorf("a legacy token's actor: %+v", n)
	}
	alice := e.owner()
	e.invite(alice, "bob", "oncall")
	toks := alice.get("/console/tokens")
	if !strings.Contains(toks.body, "Legacy") || !strings.Contains(toks.body, `action="/console/tokens/claude-code/claim"`) {
		t.Fatalf("the tokens page: %s", toks.body)
	}
	if a := alice.post("/console/tokens/claude-code/claim", url.Values{"owner": {"bob"}, "agent": {"claude-code"}, "preset": {"custom"}}); !strings.Contains(a.header.Get("Location"), "claimed") {
		t.Fatalf("claim: %q", a.header.Get("Location"))
	}
	p, ok := e.st.Authenticate(legacy)
	if !ok || p.Name() != "bob / claude-code" || !p.Has("machines:ssh") {
		t.Errorf("the claimed token: %+v %v", p, ok)
	}
	if !e.bot.told("alice claimed token claude-code as agent claude-code of bob via the console") {
		t.Errorf("chat: %v", e.bot.sent)
	}
	// its console sign-in now acts as bob's agent
	tb.post("/console/alerts/"+e.id+"/notes", url.Values{"text": {"claimed"}})
	if n := e.st.AuditLog(1)[0]; n.Actor != "bob / claude-code" {
		t.Errorf("after the claim: %+v", n)
	}
	if a := alice.post("/console/tokens/claude-code/claim", url.Values{"owner": {"bob"}}); !strings.Contains(a.header.Get("Location"), "not_legacy") {
		t.Errorf("claimed twice: %q", a.header.Get("Location"))
	}
}
