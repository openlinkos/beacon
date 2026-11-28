package web

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// TestResetSaysWhatEndsAndOffersTheTokens: the reset link's page and the page it opens say which sign-ins
// a new password ends and name the agents' tokens it leaves working, which the person may revoke with
// it. Pending reset links are listed on the People page and withdrawn there.
func TestResetSaysWhatEndsAndOffersTheTokens(t *testing.T) {
	e := newEnv(t, i18n.EN)
	alice := e.owner()
	e.invite(alice, "bob", "oncall")
	if _, err := e.st.CreateAgent(store.Agent{Name: "codex", Owner: "bob", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := e.st.CreateAgentToken("codex", 24*time.Hour, "bob")
	if err != nil {
		t.Fatal(err)
	}

	r := alice.post("/console/people/bob/reset", nil)
	if r.code != http.StatusCreated || !strings.Contains(r.body, "the OAuth sign-ins of their agents end then") || !strings.Contains(r.body, "codex") {
		t.Fatalf("the reset link's page: %d %s", r.code, r.body)
	}
	reset := shownLink(t, r.body)
	people := alice.get("/console/people")
	if !strings.Contains(people.body, "Password reset links not yet used") || !strings.Contains(people.body, `action="/console/people/links/`) {
		t.Fatalf("the People page lists no reset link: %s", people.body)
	}
	if strings.Contains(people.body, "code=") {
		t.Error("the People page shows a link's code")
	}

	b := e.browser()
	page := b.get(reset)
	if !strings.Contains(page.body, "Every console sign-in of this account ends, and so does every OAuth sign-in of its agents.") ||
		!strings.Contains(page.body, "keep working unless you revoke them: codex.") || !strings.Contains(page.body, `name="revoke_tokens"`) {
		t.Fatalf("the reset page: %s", page.body)
	}
	if a := b.fill(reset, url.Values{"password": {"bob's brand new password"}, "again": {"bob's brand new password"}, "revoke_tokens": {"1"}}); a.code != http.StatusSeeOther {
		t.Fatalf("reset: %d %s", a.code, a.body)
	}
	if _, ok := e.st.Authenticate(secret); ok {
		t.Error("the agent's token outlives a reset that revokes it")
	}

	// withdrawn from the People page: it stops at once
	r = alice.post("/console/people/bob/reset", nil)
	reset = shownLink(t, r.body)
	m := regexp.MustCompile(`action="(/console/people/links/[^"]+/withdraw)"`).FindStringSubmatch(alice.get("/console/people").body)
	if m == nil {
		t.Fatal("no withdraw action")
	}
	if a := alice.post(html.UnescapeString(m[1]), nil); !strings.Contains(a.header.Get("Location"), "link_withdrawn") {
		t.Fatalf("withdraw: %d %q", a.code, a.header.Get("Location"))
	}
	if p := e.browser().get(reset); !strings.Contains(p.body, "used already, or it is wrong") {
		t.Errorf("a withdrawn reset link: %s", p.body)
	}
	if !e.bot.told("alice withdrew a password reset link for bob via the console") {
		t.Errorf("chat: %v", e.bot.sent)
	}
}

// TestLinkOfAMakerWhoMayNoLonger: a link whose maker could no longer make it says so when opened.
func TestLinkOfAMakerWhoMayNoLonger(t *testing.T) {
	e := newEnv(t, i18n.EN)
	e.owner()
	e.token("admin", store.PeopleScopes...)
	code, _, err := e.st.CreateInvitation("viewer", "", store.LinkMaker{Name: "admin", Key: "token:admin"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/console/join?code=" + url.QueryEscape(code)
	if p := e.browser().get(path); !strings.Contains(p.body, `name="password"`) {
		t.Fatalf("the invitation before: %s", p.body)
	}
	if _, err := e.st.UpdateToken("admin", func(t *store.Token) error { t.Scopes = []string{"status:read"}; return nil }); err != nil {
		t.Fatal(err)
	}
	p := e.browser().get(path)
	if !strings.Contains(p.body, "This link no longer works: whoever made it may no longer make such a link.") || strings.Contains(p.body, `name="password"`) {
		t.Errorf("the invitation after its maker lost accounts:write: %s", p.body)
	}
}
