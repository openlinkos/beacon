package oauth_test

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

var agentFieldRe = regexp.MustCompile(`name="agent" value="([^"]*)"`)

// TestAuthorizeErrorsRedirectOnlyToAllowedClients: a request that fails its checks is answered on
// Beacon's own page, never by a redirect, unless the person signed in has allowed that client already.
// Nobody signed in is sent to sign in first, wherever the request says to go.
func TestAuthorizeErrorsRedirectOnlyToAllowedClients(t *testing.T) {
	b := start(t)
	landing := "https://client.example.com/landing"
	code, reg := register(t, b, `{"redirect_uris": ["`+landing+`"], "client_name": "Some client"}`)
	if code != http.StatusCreated {
		t.Fatalf("register: %d %v", code, reg)
	}
	client := reg["client_id"].(string)
	bad := authorizeQuery(client, landing, "s", challenge(strings.Repeat("v", 50)), "")
	bad.Del("response_type")

	br := b.browser()
	code, _, hd := br.do(http.MethodGet, "/oauth/authorize?"+bad.Encode(), nil)
	if code != http.StatusSeeOther || !strings.HasPrefix(hd.Get("Location"), "/console/sign-in?next=") {
		t.Errorf("signed out: %d %q", code, hd.Get("Location"))
	}
	// a consent form posted without a sign-in goes to sign in too
	if code, _, hd := br.do(http.MethodPost, "/oauth/authorize", bad); code != http.StatusSeeOther || !strings.HasPrefix(hd.Get("Location"), "/console/sign-in?next=") {
		t.Errorf("a form without a sign-in: %d %q", code, hd.Get("Location"))
	}

	br.signIn("bob")
	code, body, hd := br.do(http.MethodGet, "/oauth/authorize?"+bad.Encode(), nil)
	if code != http.StatusBadRequest || hd.Get("Location") != "" || !strings.Contains(body, "This sign-in cannot go on") {
		t.Errorf("a client bob never allowed: %d %q", code, hd.Get("Location"))
	}

	// bob says no on the consent page: that answer goes back, as he saw the question
	good := authorizeQuery(client, landing, "s", challenge(strings.Repeat("v", 50)), "")
	_, page, _ := br.do(http.MethodGet, "/oauth/authorize?"+good.Encode(), nil)
	form := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	form.Set("decision", "deny")
	if code, _, hd := br.do(http.MethodPost, "/oauth/authorize", form); code != http.StatusSeeOther || !strings.HasPrefix(hd.Get("Location"), landing+"?") ||
		!strings.Contains(hd.Get("Location"), "error=access_denied") {
		t.Errorf("deny: %d %q", code, hd.Get("Location"))
	}
	// a broken form posted by bob, signed in, for a client he never allowed: Beacon's page
	broken := url.Values{}
	for k, v := range form {
		broken[k] = v
	}
	broken.Del("response_type")
	broken.Set("decision", "allow")
	if code, _, hd := br.do(http.MethodPost, "/oauth/authorize", broken); code != http.StatusBadRequest || hd.Get("Location") != "" {
		t.Errorf("a broken form for a client bob never allowed: %d %q", code, hd.Get("Location"))
	}

	// once bob has allowed it, its errors go back to it
	if _, err := b.env.Store.CreateAgent(store.Agent{Name: "some-client", Owner: "bob", Preset: "oncall", Client: client}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.env.Store.CreateGrant(store.Grant{Agent: "some-client", Client: client, ClientName: "Some client", Preset: "oncall", Expires: time.Now().Add(24 * time.Hour)}, true); err != nil {
		t.Fatal(err)
	}
	code, _, hd = br.do(http.MethodGet, "/oauth/authorize?"+bad.Encode(), nil)
	if to, _ := url.Parse(hd.Get("Location")); code != http.StatusSeeOther || !strings.HasPrefix(hd.Get("Location"), landing) || to.Query().Get("error") != "unsupported_response_type" {
		t.Errorf("a client bob allowed: %d %q", code, hd.Get("Location"))
	}
	// alice has not
	alice := b.browser()
	alice.signIn("alice")
	if code, _, hd := alice.do(http.MethodGet, "/oauth/authorize?"+bad.Encode(), nil); code != http.StatusBadRequest || hd.Get("Location") != "" {
		t.Errorf("a client only bob allowed, for alice: %d %q", code, hd.Get("Location"))
	}
}

// TestDocumentsFetchedOnlyForSignedInPeople: Beacon fetches a client ID metadata document only for a
// request a person signed in to the console makes.
func TestDocumentsFetchedOnlyForSignedInPeople(t *testing.T) {
	b := start(t)
	var fetched atomic.Int32
	var docURL string
	doc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"client_id": "`+docURL+`", "client_name": "Codex", "redirect_uris": ["http://127.0.0.1/callback"]}`)
	}))
	defer doc.Close()
	docURL = doc.URL + "/client.json"
	b.oauth.Fetch = doc.Client()
	q := authorizeQuery(docURL, "http://127.0.0.1:5/callback", "s", challenge(strings.Repeat("v", 50)), "")

	anon := b.browser()
	anon.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	anon.do(http.MethodPost, "/oauth/authorize", q)
	tb := b.browser()
	secret := b.env.Token(t, "legacy", "status:read")
	_, page, _ := tb.do(http.MethodGet, "/console/sign-in?with=token", nil)
	tb.do(http.MethodPost, "/console/sign-in", url.Values{"token": {secret}, "form": {formRe.FindStringSubmatch(page)[1]}})
	tb.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	if n := fetched.Load(); n != 0 {
		t.Fatalf("the document was fetched %d times for requests no person signed in to", n)
	}
	br := b.browser()
	br.signIn("bob")
	if code, body, _ := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil); code != http.StatusOK || !strings.Contains(body, "Codex wants to use Beacon as you") {
		t.Fatalf("signed in: %d %s", code, body)
	}
	if n := fetched.Load(); n != 1 {
		t.Errorf("fetched %d times for bob", n)
	}
}

// TestConsentPageSaysWhoAsks: the consent page never offers, by default, an existing agent made for
// another client, says where what the client says about itself comes from, and makes plain when the
// answer goes to a website.
func TestConsentPageSaysWhoAsks(t *testing.T) {
	b := start(t)
	br := b.browser()
	br.signIn("bob")
	if _, err := b.env.Store.CreateAgent(store.Agent{Name: "claude-code", Owner: "bob", Preset: "oncall"}); err != nil {
		t.Fatal(err)
	}
	landing := "https://client.example.com/landing"
	_, reg := register(t, b, `{"redirect_uris": ["`+landing+`"], "client_name": "Claude Code"}`)
	client := reg["client_id"].(string)
	q := authorizeQuery(client, landing, "s", challenge(strings.Repeat("v", 50)), "")
	code, page, _ := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	if code != http.StatusOK {
		t.Fatalf("consent: %d %s", code, page)
	}
	m := agentFieldRe.FindStringSubmatch(page)
	if m == nil || m[1] == "claude-code" || !strings.HasPrefix(m[1], "claude-code-") {
		t.Errorf("the agent offered for a client that names itself after an existing agent: %v", m)
	}
	if !strings.Contains(page, "It registered itself with Beacon at") || !strings.Contains(page, "anyone can register a client under any name") {
		t.Errorf("the page does not say the client registered itself: %s", page)
	}
	if !strings.Contains(page, "to the website https://client.example.com, not to a program on this computer") {
		t.Errorf("the page does not make plain the answer goes to a website: %s", page)
	}

	// an agent made for this client is offered again for it
	to, _ := br.consent("/oauth/authorize?"+q.Encode(), url.Values{"preset": {"readonly"}})
	if to.Host != "client.example.com" {
		t.Fatalf("allow: %s", to)
	}
	made := ""
	for _, a := range b.env.Store.Agents("bob") {
		if a.Client == client {
			made = a.Name
		}
	}
	if made == "" || made == "claude-code" {
		t.Fatalf("the agent made for the client: %q", made)
	}
	_, page, _ = br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	if m := agentFieldRe.FindStringSubmatch(page); m == nil || m[1] != made {
		t.Errorf("the agent made for this client is not offered again: %v, want %s", m, made)
	}

	// a client on the loopback: no warning about a website
	_, reg = register(t, b, `{"redirect_uris": ["http://127.0.0.1:41000/callback"], "client_name": "Local"}`)
	_, page, _ = br.do(http.MethodGet, "/oauth/authorize?"+authorizeQuery(reg["client_id"].(string), "http://127.0.0.1:41000/callback", "s", challenge(strings.Repeat("v", 50)), "").Encode(), nil)
	if strings.Contains(page, "not to a program on this computer") || !strings.Contains(page, "Whatever you answer, Beacon sends you back to http://127.0.0.1:41000") {
		t.Errorf("a loopback client's page: %s", page)
	}

	// a client named by its metadata document: the page names the document's host
	var docURL string
	doc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"client_id": "`+docURL+`", "client_name": "Claude Code", "redirect_uris": ["http://localhost/callback"]}`)
	}))
	defer doc.Close()
	docURL = doc.URL + "/client.json"
	b.oauth.Fetch = doc.Client()
	host := strings.TrimPrefix(doc.URL, "https://")
	_, page, _ = br.do(http.MethodGet, "/oauth/authorize?"+authorizeQuery(docURL, "http://localhost:5555/callback", "s", challenge(strings.Repeat("v", 50)), "").Encode(), nil)
	if !strings.Contains(page, "what a document on "+host+" says") {
		t.Errorf("a document client's page does not name the document's host %s: %s", host, page)
	}
	if m := agentFieldRe.FindStringSubmatch(page); m == nil || m[1] == "claude-code" || m[1] == made {
		t.Errorf("the agent offered to another client of the same name: %v", m)
	}
}
