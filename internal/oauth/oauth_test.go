package oauth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/mcpserver"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/oauth"
	"github.com/openlinkos/beacon/internal/server"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
	"github.com/openlinkos/beacon/internal/web"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestMain(m *testing.M) {
	store.PasswordParams = store.Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

// beacon is the service on a loopback address, as an MCP client reaches it: the router with the API,
// MCP, the console and the authorization server, and alice, an owner, and bob, on call.
type beacon struct {
	t     *testing.T
	url   string
	env   *testutil.Env
	oauth *oauth.Server
}

func start(t *testing.T) *beacon {
	t.Helper()
	e := testutil.New(t)
	b := &beacon{t: t, env: e}
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	b.url = srv.URL
	a := &api.Auth{Store: e.Store}
	b.oauth = &oauth.Server{Store: e.Store, Bridge: e.Bridge, Auth: a, PublicURL: b.url, Log: slog.Default()}
	w := &web.Server{Bridge: e.Bridge, Auth: a, Pages: &link.Pages{PublicURL: b.url}, Lang: i18n.EN, Location: time.UTC, Store: e.Store, OAuth: b.oauth}
	srv.Config.Handler = server.Handler(server.Deps{
		Bridge: e.Bridge, API: &api.Server{Bridge: e.Bridge, Auth: a, Log: slog.Default()},
		MCP:     mcpserver.Handler(e.Bridge, mcpserver.SSH{}, a, slog.Default(), mcpserver.WithResourceMetadata(b.oauth.ResourceMetadataURL)),
		Metrics: metrics.New("test"), Log: slog.Default(), Web: w, OAuth: b.oauth,
	})
	code, _, err := e.Store.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Bridge.Join(context.Background(), code, "alice", "Alice", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	inv, err := e.Bridge.Invite(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, "oncall", "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(inv.URL)
	if _, err := e.Bridge.Join(context.Background(), u.Query().Get("code"), "bob", "Bob", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	return b
}

// browser is a person's browser: the console's cookies, kept by hand (they are Secure, and the test
// server is plain http on the loopback).
type browser struct {
	b       *beacon
	cookies map[string]string
}

var (
	formRe   = regexp.MustCompile(`name="form" value="([^"]+)"`)
	hiddenRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)
)

func (b *beacon) browser() *browser { return &browser{b: b, cookies: map[string]string{}} }

func (br *browser) do(method, path string, form url.Values) (int, string, http.Header) {
	br.b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	u := path
	if strings.HasPrefix(path, "/") {
		u = br.b.url + path
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		br.b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range br.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		br.b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(br.cookies, c.Name)
		} else {
			br.cookies[c.Name] = c.Value
		}
	}
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out), res.Header
}

// signIn signs the browser in as username.
func (br *browser) signIn(username string) {
	br.b.t.Helper()
	_, page, _ := br.do(http.MethodGet, "/console/sign-in", nil)
	m := formRe.FindStringSubmatch(page)
	if m == nil {
		br.b.t.Fatalf("no sign-in form: %s", page)
	}
	if code, body, _ := br.do(http.MethodPost, "/console/sign-in", url.Values{"username": {username}, "password": {"correct horse battery"}, "form": {m[1]}}); code != http.StatusSeeOther {
		br.b.t.Fatalf("sign in as %s: %d %s", username, code, body)
	}
}

// consent opens an authorization URL and answers the consent page with choices (decision allow or deny,
// agent, preset, duration…), returning where Beacon sends the browser.
func (br *browser) consent(authURL string, choices url.Values) (*url.URL, string) {
	br.b.t.Helper()
	code, page, _ := br.do(http.MethodGet, authURL, nil)
	if code != http.StatusOK {
		br.b.t.Fatalf("authorize: %d %s", code, page)
	}
	form := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	form.Set("decision", "allow")
	form.Set("duration", "1")
	if m := regexp.MustCompile(`name="agent" value="([^"]*)"`).FindStringSubmatch(page); m != nil {
		form.Set("agent", m[1])
	}
	if m := regexp.MustCompile(`name="preset" value="([^"]+)" checked`).FindStringSubmatch(page); m != nil {
		form.Set("preset", m[1])
	}
	for k, v := range choices {
		form[k] = v
	}
	code, body, hd := br.do(http.MethodPost, "/oauth/authorize", form)
	if code != http.StatusSeeOther {
		br.b.t.Fatalf("consent: %d %s", code, body)
	}
	to, err := url.Parse(hd.Get("Location"))
	if err != nil {
		br.b.t.Fatal(err)
	}
	return to, page
}

func getJSON(t *testing.T, u string, v any) http.Header {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", u, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
	return res.Header
}

func TestMetadataAndChallenge(t *testing.T) {
	b := start(t)
	var prm map[string]any
	hd := getJSON(t, b.url+"/.well-known/oauth-protected-resource/mcp", &prm)
	if prm["resource"] != b.url+"/mcp" || prm["authorization_servers"].([]any)[0] != b.url || hd.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("protected resource metadata: %v", prm)
	}
	getJSON(t, b.url+"/.well-known/oauth-protected-resource", &prm)
	if prm["resource"] != b.url {
		t.Errorf("root resource metadata: %v", prm)
	}
	var asm map[string]any
	getJSON(t, b.url+"/.well-known/oauth-authorization-server", &asm)
	for k, want := range map[string]any{"issuer": b.url, "authorization_endpoint": b.url + "/oauth/authorize", "token_endpoint": b.url + "/oauth/token",
		"registration_endpoint": b.url + "/oauth/register", "revocation_endpoint": b.url + "/oauth/revoke",
		"client_id_metadata_document_supported": true, "authorization_response_iss_parameter_supported": true} {
		if asm[k] != want {
			t.Errorf("%s = %v, want %v", k, asm[k], want)
		}
	}
	for k, want := range map[string]string{"code_challenge_methods_supported": "S256", "grant_types_supported": "authorization_code refresh_token",
		"response_types_supported": "code", "token_endpoint_auth_methods_supported": "none", "scopes_supported": "readonly oncall operator offline_access"} {
		var got []string
		for _, v := range asm[k].([]any) {
			got = append(got, v.(string))
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%s = %v, want %s", k, got, want)
		}
	}

	// /mcp without a credential names where to sign in
	res, err := http.Post(b.url+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || res.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+b.url+`/.well-known/oauth-protected-resource/mcp"` {
		t.Errorf("/mcp: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
}

// fetcher answers the authorization URL as a person would: signed in as who, allowing it.
func (br *browser) fetcher(choices url.Values) auth.AuthorizationCodeFetcher {
	return func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		to, _ := br.consent(args.URL, choices)
		q := to.Query()
		if q.Get("error") != "" {
			return nil, errors.New(q.Get("error"))
		}
		return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
	}
}

type whoami struct {
	Actor      string   `json:"actor"`
	User       string   `json:"user"`
	Agent      string   `json:"agent"`
	Preset     string   `json:"preset"`
	Credential string   `json:"credential"`
	Client     string   `json:"client"`
	Scopes     []string `json:"scopes"`
}

func callWhoami(t *testing.T, cs *mcp.ClientSession) whoami {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil || res.IsError {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	var w whoami
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &w)
	return w
}

// TestMCPClientSignsIn: an MCP client (the SDK's, as Claude Code's does) adds Beacon's /mcp, registers
// itself, sends the person to allow it, and acts as their agent.
func TestMCPClientSignsIn(t *testing.T) {
	b := start(t)
	br := b.browser()
	br.signIn("bob")
	h, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{"http://localhost:45678/callback"}, ClientName: "Claude Code", TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}}},
		AuthorizationCodeFetcher: br.fetcher(nil), RequestRefreshToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: b.url + "/mcp", OAuthHandler: h, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	w := callWhoami(t, cs)
	if w.Actor != "bob / claude-code" || w.Credential != "oauth" || w.Client != "Claude Code" || w.Preset != "oncall" || !strings.Contains(strings.Join(w.Scopes, " "), "machines:ssh") {
		t.Errorf("whoami: %+v", w)
	}
	a, ok := b.env.Store.Agent("claude-code")
	if !ok || a.Owner != "bob" || a.Client == "" {
		t.Fatalf("agent: %+v", a)
	}
	grants := b.env.Store.Grants("claude-code")
	if len(grants) != 1 || !strings.Contains(grants[0].Scope, "offline_access") || grants[0].Expires.Before(time.Now().Add(29*24*time.Hour)) {
		t.Errorf("grants: %+v", grants)
	}
	if entries := b.env.Store.AuditLog(20); !hasAudit(entries, "bob", "oauth_allow", "claude-code") {
		t.Errorf("audit: %+v", entries)
	}
	b.env.Bot.Texts = append(b.env.Bot.Texts, "")
	if !strings.Contains(strings.Join(b.env.Bot.Texts, "\n"), "bob allowed Claude Code to use Beacon as agent claude-code (On call)") {
		t.Errorf("chat: %v", b.env.Bot.Texts)
	}
	// what the agent does is recorded as bob's agent
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ack_alert", Arguments: map[string]any{"id": testutil.GroupID(), "note": "on it"}})
	if err != nil || res.IsError {
		t.Fatalf("ack: %v %+v", err, res)
	}
	if !hasAudit(b.env.Store.AuditLog(5), "bob / claude-code", "ack", testutil.GroupID()) {
		t.Errorf("the ack's actor: %+v", b.env.Store.AuditLog(5))
	}

	// demoting bob limits the agent at once, on the very same access token
	if _, err := b.env.Bridge.UpdateUser(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, "bob", bridge.UserChange{Role: ptr("viewer")}); err != nil {
		t.Fatal(err)
	}
	if w := callWhoami(t, cs); strings.Contains(strings.Join(w.Scopes, " "), "alerts:write") {
		t.Errorf("after demoting bob: %v", w.Scopes)
	}
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "add_note", Arguments: map[string]any{"id": testutil.GroupID(), "text": "x"}})
	if err == nil && !res.IsError {
		t.Error("a demoted owner's agent still writes")
	}
	// revoking its sign-in on the console's behalf ends it
	if err := b.env.Bridge.RevokeGrant(context.Background(), bridge.Actor{Name: "cli:root", Via: "cli"}, grants[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(b.env.Store.Grants("")) != 0 {
		t.Error("the grant outlives revoking it")
	}
}

func ptr[T any](v T) *T { return &v }

func hasAudit(entries []store.AuditEntry, actor, action, target string) bool {
	for _, e := range entries {
		if e.Actor == actor && e.Action == action && e.Target == target {
			return true
		}
	}
	return false
}

// TestClientIDMetadataDocument: a client whose client_id is an https URL needs no registration; Beacon
// fetches its document and honours its redirect URIs.
func TestClientIDMetadataDocument(t *testing.T) {
	b := start(t)
	var docURL string
	doc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"client_id": "`+docURL+`", "client_name": "Codex", "redirect_uris": ["http://127.0.0.1/callback"], "token_endpoint_auth_method": "none"}`)
		case "/other.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"client_id": "https://elsewhere.example.com/x.json", "redirect_uris": ["http://127.0.0.1/callback"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer doc.Close()
	docURL = doc.URL + "/client.json"
	b.oauth.Fetch = doc.Client()
	br := b.browser()
	br.signIn("alice")
	h, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: docURL},
		RedirectURL:                    "http://127.0.0.1:53111/callback", // any port on the loopback (RFC 8252 section 7.3)
		AuthorizationCodeFetcher:       br.fetcher(url.Values{"preset": {"readonly"}}), RequestRefreshToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: b.url + "/mcp", OAuthHandler: h, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	if w := callWhoami(t, cs); w.Actor != "alice / codex" || w.Preset != "readonly" || w.Client != "Codex" || strings.Contains(strings.Join(w.Scopes, " "), "alerts:write") {
		t.Errorf("whoami: %+v", w)
	}

	// a document naming another client, an unknown path, a plain http URL: Beacon stops at its own page
	for _, id := range []string{doc.URL + "/other.json", doc.URL + "/missing.json", "http://127.0.0.1/client.json"} {
		q := authorizeQuery(id, "http://127.0.0.1:5/callback", "s", challenge("v"+strings.Repeat("x", 50)), "")
		code, body, hd := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
		if code != http.StatusBadRequest || hd.Get("Location") != "" || !strings.Contains(body, "This sign-in cannot go on") {
			t.Errorf("client_id %s: %d %q", id, code, hd.Get("Location"))
		}
	}

	// oauth.client_id_documents: false (a Beacon that cannot reach the internet): not offered, not taken
	b.oauth.NoDocuments = true
	var asm map[string]any
	getJSON(t, b.url+"/.well-known/oauth-authorization-server", &asm)
	if asm["client_id_metadata_document_supported"] != false {
		t.Errorf("documents still offered: %v", asm["client_id_metadata_document_supported"])
	}
	q := authorizeQuery(docURL, "http://127.0.0.1:5/callback", "s", challenge("v"+strings.Repeat("x", 50)), "")
	if code, _, _ := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil); code != http.StatusBadRequest {
		t.Errorf("a document taken with documents off: %d", code)
	}
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeQuery(client, redirect, state, challenge, scope string) url.Values {
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if scope != "" {
		q.Set("scope", scope)
	}
	return q
}

func register(t *testing.T, b *beacon, body string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(b.url+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// TestTokenLockoutNamesTheWait checks the token endpoint names the wait and sets Retry-After, and keeps
// the OAuth error code too_many_requests.
func TestTokenLockoutNamesTheWait(t *testing.T) {
	e := testutil.New(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	o := &oauth.Server{Store: e.Store, Auth: &api.Auth{Store: e.Store, Now: func() time.Time { return now }}, PublicURL: "https://beacon.example.com", Log: slog.Default()}
	mux := http.NewServeMux()
	o.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	post := func() (int, string, string) {
		t.Helper()
		res, err := http.PostForm(srv.URL+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"nope"}, "client_id": {"lbc_test"}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		desc, _ := out["error_description"].(string)
		errCode, _ := out["error"].(string)
		return res.StatusCode, errCode + " " + desc, res.Header.Get("Retry-After")
	}
	if code, body, retry := post(); code != http.StatusBadRequest || !strings.Contains(body, "invalid_grant") || retry != "" {
		t.Fatalf("one bad refresh: %d Retry-After=%q %s", code, retry, body)
	}
	for range 19 {
		post()
	}
	code, body, retry := post()
	if code != http.StatusTooManyRequests || !strings.Contains(body, "too_many_requests") || !strings.Contains(body, "too many failed attempts from this address; try again in 10 minutes") || retry != "600" {
		t.Fatalf("lockout: %d Retry-After=%q %s", code, retry, body)
	}
}

func token(t *testing.T, b *beacon, form url.Values) (int, map[string]any) {
	t.Helper()
	res, err := http.PostForm(b.url+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the token endpoint's answer may be cached: %q", res.Header.Get("Cache-Control"))
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// TestFlowChecks walks the protocol by hand: registration's checks, the consent page, PKCE, one-time
// codes, refresh rotation and replays, revocation, resources and redirects.
func TestFlowChecks(t *testing.T) {
	b := start(t)
	redirect := "http://127.0.0.1:41000/callback"

	// registration: public clients on the loopback or https only
	for body, want := range map[string]string{
		`{"redirect_uris": ["http://example.com/cb"]}`:    "invalid_redirect_uri",
		`{"redirect_uris": ["myapp://cb"]}`:               "invalid_redirect_uri",
		`{"redirect_uris": ["https://example.com/cb#x"]}`: "invalid_redirect_uri",
		`{"redirect_uris": []}`:                           "invalid_redirect_uri",
		`{"redirect_uris": ["https://example.com/cb"], "token_endpoint_auth_method": "client_secret_basic"}`: "invalid_client_metadata",
		`not json`: "invalid_client_metadata",
	} {
		if code, out := register(t, b, body); code != http.StatusBadRequest || out["error"] != want {
			t.Errorf("register %s: %d %v", body, code, out)
		}
	}
	code, reg := register(t, b, `{"redirect_uris": ["`+redirect+`"], "client_name": "Test <client>", "grant_types": ["authorization_code", "refresh_token"]}`)
	if code != http.StatusCreated || reg["token_endpoint_auth_method"] != "none" || reg["client_secret"] != nil {
		t.Fatalf("register: %d %v", code, reg)
	}
	client := reg["client_id"].(string)

	br := b.browser()
	verifier := strings.Repeat("v", 50)
	q := authorizeQuery(client, redirect, "st&ate", challenge(verifier), "oncall offline_access")
	q.Set("resource", b.url+"/mcp")

	// signed out: sign in first, then back here
	if code, _, hd := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil); code != http.StatusSeeOther || !strings.HasPrefix(hd.Get("Location"), "/console/sign-in?next=") {
		t.Errorf("signed out: %d %q", code, hd.Get("Location"))
	}
	// signed in with a token: not a person, no consent
	tb := b.browser()
	secret := b.env.Token(t, "legacy", "status:read")
	_, page, _ := tb.do(http.MethodGet, "/console/sign-in?with=token", nil)
	tb.do(http.MethodPost, "/console/sign-in", url.Values{"token": {secret}, "form": {formRe.FindStringSubmatch(page)[1]}})
	if code, body, _ := tb.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil); code != http.StatusForbidden || !strings.Contains(body, "Sign in with your account") {
		t.Errorf("a token's sign-in: %d", code)
	}

	br.signIn("bob")
	// a wrong redirect, an unknown client: nothing is sent anywhere
	for _, bad := range []url.Values{authorizeQuery(client, "http://127.0.0.1:41000/elsewhere", "s", challenge(verifier), ""),
		authorizeQuery(client, "https://evil.example.com/callback", "s", challenge(verifier), ""), authorizeQuery("lbc_nobody", redirect, "s", challenge(verifier), "")} {
		if code, _, hd := br.do(http.MethodGet, "/oauth/authorize?"+bad.Encode(), nil); code != http.StatusBadRequest || hd.Get("Location") != "" {
			t.Errorf("%v: %d %q", bad, code, hd.Get("Location"))
		}
	}
	// a wrong resource, no PKCE, plain PKCE: back to the client with the error
	wrong := authorizeQuery(client, redirect, "s1", challenge(verifier), "")
	wrong.Set("resource", "https://other.example.com/mcp")
	noPKCE := authorizeQuery(client, redirect, "s2", "", "")
	plain := authorizeQuery(client, redirect, "s3", challenge(verifier), "")
	plain.Set("code_challenge_method", "plain")
	for want, bad := range map[string]url.Values{"invalid_target": wrong, "invalid_request": noPKCE, "invalid_request ": plain} {
		code, _, hd := br.do(http.MethodGet, "/oauth/authorize?"+bad.Encode(), nil)
		to, _ := url.Parse(hd.Get("Location"))
		if code != http.StatusSeeOther || to.Query().Get("error") != strings.TrimSpace(want) || to.Query().Get("state") != bad.Get("state") || to.Query().Get("iss") != b.url {
			t.Errorf("%s: %d %q", want, code, hd.Get("Location"))
		}
	}

	// the consent page: who asks, where it sends you back, the preset within bob's role
	_, page, hd := br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	if !strings.Contains(page, "Test &lt;client&gt; wants to use Beacon as you") || !strings.Contains(page, "http://127.0.0.1:41000") ||
		strings.Contains(page, `value="operator"`) || !strings.Contains(page, `value="oncall" checked`) || strings.Contains(page, "<client>") {
		t.Errorf("consent page: %s", page)
	}
	if csp := hd.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' http://127.0.0.1:41000") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("consent CSP: %q", csp)
	}
	// a stronger preset than bob's role: refused
	form := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	strong := cloneValues(form)
	strong.Set("decision", "allow")
	strong.Set("agent", "test-client")
	strong.Set("preset", "operator")
	if code, _, _ := br.do(http.MethodPost, "/oauth/authorize", strong); code != http.StatusBadRequest {
		t.Errorf("a stronger preset: %d", code)
	}
	// without the CSRF secret: refused
	nocsrf := cloneValues(form)
	nocsrf.Del("csrf")
	nocsrf.Set("decision", "allow")
	if code, _, _ := br.do(http.MethodPost, "/oauth/authorize", nocsrf); code != http.StatusForbidden {
		t.Errorf("without the CSRF secret: %d", code)
	}
	// deny: back to the client
	deny := cloneValues(form)
	deny.Set("decision", "deny")
	_, _, hd = br.do(http.MethodPost, "/oauth/authorize", deny)
	if to, _ := url.Parse(hd.Get("Location")); to.Query().Get("error") != "access_denied" || to.Query().Get("state") != "st&ate" {
		t.Errorf("deny: %q", hd.Get("Location"))
	}

	to, _ := br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"test-client"}})
	if to.Host != "127.0.0.1:41000" || to.Query().Get("state") != "st&ate" || to.Query().Get("iss") != b.url {
		t.Fatalf("allow: %s", to)
	}
	code1 := to.Query().Get("code")
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code1}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}}

	// PKCE: a wrong verifier spends the code
	bad := cloneValues(exchange)
	bad.Set("code_verifier", strings.Repeat("w", 50))
	if code, out := token(t, b, bad); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("a wrong verifier: %d %v", code, out)
	}
	if code, _ := token(t, b, exchange); code != http.StatusBadRequest {
		t.Error("a code works after a failed exchange")
	}

	// a fresh code: the right exchange, then its replay revokes what it gave
	to, _ = br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"test-client"}})
	exchange.Set("code", to.Query().Get("code"))
	wrongRes := cloneValues(exchange)
	wrongRes.Set("resource", "https://other.example.com/mcp")
	if code, out := token(t, b, wrongRes); code != http.StatusBadRequest || out["error"] != "invalid_target" {
		t.Errorf("a wrong resource: %d %v", code, out)
	}
	code, tok := token(t, b, exchange)
	if code != http.StatusOK || tok["token_type"] != "Bearer" || tok["scope"] != "oncall offline_access" || !strings.HasPrefix(tok["access_token"].(string), "lba_") ||
		!strings.HasPrefix(tok["refresh_token"].(string), "lbr_") || tok["expires_in"].(float64) > 3600 {
		t.Fatalf("exchange: %d %v", code, tok)
	}
	access := tok["access_token"].(string)
	if p, ok := b.env.Store.Authenticate(access); !ok || p.Name() != "bob / test-client" {
		t.Errorf("the access token: %+v %v", p, ok)
	}
	// the REST API takes the access token too
	req, _ := http.NewRequest(http.MethodGet, b.url+"/api/v1/token", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusOK {
		t.Errorf("REST with the access token: %v %v", res, err)
	} else {
		_ = res.Body.Close()
	}
	if code, _ := token(t, b, exchange); code != http.StatusBadRequest {
		t.Error("a code worked twice")
	}
	if _, ok := b.env.Store.Authenticate(access); ok {
		t.Error("the tokens of a replayed code still work")
	}

	// refresh: rotation, and a replaced refresh token revokes the grant
	to, _ = br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"test-client"}})
	exchange.Set("code", to.Query().Get("code"))
	_, tok = token(t, b, exchange)
	refresh := tok["refresh_token"].(string)
	code, next := token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {client}})
	if code != http.StatusOK || next["refresh_token"] == refresh || next["access_token"] == tok["access_token"] {
		t.Fatalf("refresh: %d %v", code, next)
	}
	if code, _ := token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"lbc_other"}}); code != http.StatusBadRequest {
		t.Error("another client refreshed")
	}
	if code, out := token(t, b, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {client}}); code != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("a replaced refresh token: %d %v", code, out)
	}
	if _, ok := b.env.Store.Authenticate(next["access_token"].(string)); ok {
		t.Error("the grant outlives a replayed refresh token")
	}
	if !strings.Contains(strings.Join(b.env.Bot.Texts, "\n"), "A used refresh token of agent test-client") {
		t.Errorf("the chat was not told of the replay: %v", b.env.Bot.Texts)
	}

	// revocation (RFC 7009): 200 whatever the token, and a refresh token ends its grant
	to, _ = br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"test-client"}})
	exchange.Set("code", to.Query().Get("code"))
	_, tok = token(t, b, exchange)
	for _, tk := range []string{"lbr_unknown", tok["refresh_token"].(string)} {
		res, err := http.PostForm(b.url+"/oauth/revoke", url.Values{"token": {tk}, "client_id": {client}})
		if err != nil || res.StatusCode != http.StatusOK {
			t.Errorf("revoke: %v %v", res, err)
		} else {
			_ = res.Body.Close()
		}
	}
	if _, ok := b.env.Store.Authenticate(tok["access_token"].(string)); ok {
		t.Error("tokens outlive revoking their refresh token")
	}
	// the consent reused bob's agent by its name: one agent, every sign-in its own grant
	if ags := b.env.Store.Agents("bob"); len(ags) != 1 || ags[0].Name != "test-client" {
		t.Errorf("bob's agents: %+v", ags)
	}

	// allowing a client never widens an agent: a read-only agent stays read-only, and a grant for it may
	// not ask for more; each grant keeps what its own consent allowed
	to, _ = br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"reader"}, "preset": {"readonly"}})
	exchange.Set("code", to.Query().Get("code"))
	_, tok = token(t, b, exchange)
	readerAccess := tok["access_token"].(string)
	_, page, _ = br.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	form = url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	form.Set("decision", "allow")
	form.Set("agent", "reader")
	form.Set("preset", "oncall")
	if code, body, _ := br.do(http.MethodPost, "/oauth/authorize", form); code != http.StatusBadRequest || !strings.Contains(body, "That did not work") {
		t.Errorf("a stronger preset for a read-only agent: %d", code)
	}
	if a, _ := b.env.Store.Agent("reader"); a.Preset != "readonly" {
		t.Errorf("the agent was widened: %+v", a)
	}
	if p, _ := b.env.Store.Authenticate(readerAccess); p.Has("alerts:write") {
		t.Errorf("the read-only grant: %v", p.Scopes)
	}

	// a client with one redirect URI may leave it out of both requests
	noRedirect := authorizeQuery(client, "", "s4", challenge(verifier), "")
	noRedirect.Del("redirect_uri")
	to, _ = br.consent("/oauth/authorize?"+noRedirect.Encode(), url.Values{"agent": {"test-client"}})
	if to.Host != "127.0.0.1:41000" {
		t.Fatalf("without redirect_uri: %s", to)
	}
	left := cloneValues(exchange)
	left.Set("code", to.Query().Get("code"))
	left.Del("redirect_uri")
	if code, out := token(t, b, left); code != http.StatusOK {
		t.Errorf("an exchange without redirect_uri, as the request had none: %d %v", code, out)
	}
	// but one that named it must name it again
	to, _ = br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"test-client"}})
	left.Set("code", to.Query().Get("code"))
	if code, _ := token(t, b, left); code != http.StatusBadRequest {
		t.Errorf("an exchange without the redirect_uri its request named: %d", code)
	}

	// a code expires after 60 seconds
	now := time.Now()
	b.oauth.Now = func() time.Time { return now }
	to, _ = br.consent("/oauth/authorize?"+q.Encode(), url.Values{"agent": {"test-client"}})
	exchange.Set("code", to.Query().Get("code"))
	now = now.Add(61 * time.Second)
	if code, _ := token(t, b, exchange); code != http.StatusBadRequest {
		t.Error("an expired code worked")
	}

	// without offline_access (Claude Code does not ask for it): a refresh token all the same, for as long
	// as the person chose (here 90 days)
	b.oauth.Now = nil
	short := authorizeQuery(client, redirect, "s", challenge(verifier), "")
	to, page = br.consent("/oauth/authorize?"+short.Encode(), url.Values{"agent": {"test-client"}, "preset": {"oncall"}, "duration": {"2"}})
	if !strings.Contains(page, `name="duration" value="2"`) {
		t.Error("the consent page does not offer how long")
	}
	exchange.Set("code", to.Query().Get("code"))
	if code, tok := token(t, b, exchange); code != http.StatusOK || tok["refresh_token"] == nil || tok["scope"] != "oncall" {
		t.Errorf("without offline_access: %d %v", code, tok)
	}
	if g := b.env.Store.Grants("test-client"); len(g) == 0 || g[0].Expires.Before(time.Now().Add(89*24*time.Hour)) {
		t.Errorf("the grant's end: %+v", g)
	}

	// registrations are rate-limited per address
	limited := false
	for range 12 {
		if code, _ := register(t, b, `{"redirect_uris": ["`+redirect+`"]}`); code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Error("registrations are not rate-limited")
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
