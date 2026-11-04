// Package oauth is Beacon's own authorization server for its MCP endpoint, as the MCP authorization
// specification asks: an agent's client (Claude Code, Codex) adds Beacon's /mcp, gets a 401 that names
// the protected resource metadata (RFC 9728), finds the authorization server's metadata (RFC 8414),
// registers itself (RFC 7591) or names its client ID metadata document, and sends the person to
// /oauth/authorize, where they sign in to the console and allow it (package web, which uses Parse and
// Code here). The client then exchanges the code (PKCE S256, one-time, valid 60 seconds) for an access
// token valid an hour and a refresh token valid as long as the person allowed, whether or not the client
// asked for offline_access (MCP clients refresh by themselves, and Claude Code does not ask for it);
// every refresh replaces the refresh token, and sending a replaced one revokes the grant. Revocation is
// RFC 7009's.
//
// Beacon is the only authorization server: people sign in with Beacon's own accounts and nothing else.
// Access tokens are opaque, stored as their SHA-256 (package store), and accepted wherever a bearer
// token is: the REST API and MCP. What one may do is its agent's: the preset allowed, within the
// person's role, read afresh on every request.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// Paths of the endpoints.
const (
	ResourcePath     = "/.well-known/oauth-protected-resource"
	MetadataPath     = "/.well-known/oauth-authorization-server"
	AuthorizePath    = "/oauth/authorize"
	TokenPath        = "/oauth/token" //nolint:gosec // G101: a path, not a credential
	RegisterPath     = "/oauth/register"
	RevokePath       = "/oauth/revoke"
	MCPPath          = "/mcp"
	OfflineAccess    = "offline_access"
	codeTTL          = 60 * time.Second
	spentTTL         = 10 * time.Minute
	maxRedirectURIs  = 10
	maxURL           = 2000
	registerLimit    = 10
	registerWindow   = 10 * time.Minute
	maxPendingCodes  = 10000
	documentationURL = "https://beacon.openlinkos.com/guide/people"
)

// Durations are how long a person may allow a client, as the consent page offers them.
var Durations = []time.Duration{24 * time.Hour, 30 * 24 * time.Hour, 90 * 24 * time.Hour}

// Server speaks the protocol. PublicURL is the issuer (Beacon's public_url; empty: the request's own
// scheme and host). Auth counts failed token requests with the API's limits; Fetch fetches client ID
// metadata documents (nil: a client that refuses private addresses, see cimd.go), and NoDocuments turns
// them off (oauth.client_id_documents: false), for a Beacon that cannot reach the internet.
type Server struct {
	Store       *store.Store
	Bridge      *bridge.Bridge
	Auth        *api.Auth
	PublicURL   string
	Log         *slog.Logger
	Now         func() time.Time
	Fetch       *http.Client
	NoDocuments bool

	mu    sync.Mutex
	codes map[string]*pending // by the code's SHA-256
	spent map[string]time.Time
	regs  map[netip.Addr]*window
	docs  map[string]cached // client ID metadata documents, by URL
}

type window struct {
	n     int
	start time.Time
}

// pending is an authorization code waiting to be exchanged.
type pending struct {
	Request
	Agent    string
	Preset   string
	Machines []string
	Until    time.Time // when the grant ends
	Expires  time.Time // when the code does
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Issuer is Beacon's identifier as an authorization server, and the base of its endpoints.
func (s *Server) Issuer(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimSuffix(s.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ResourceMetadataURL is the protected resource metadata of /mcp, which a 401 from it names.
func (s *Server) ResourceMetadataURL(r *http.Request) string {
	return s.Issuer(r) + ResourcePath + MCPPath
}

// Register adds the metadata, registration, token and revocation endpoints to mux; /oauth/authorize is
// the console's (package web).
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+ResourcePath, s.resourceMetadata(""))
	mux.HandleFunc("GET "+ResourcePath+MCPPath, s.resourceMetadata(MCPPath))
	mux.HandleFunc("GET "+MetadataPath, s.metadata)
	mux.HandleFunc("OPTIONS /.well-known/", preflight)
	mux.HandleFunc("POST "+RegisterPath, s.register)
	mux.HandleFunc("POST "+TokenPath, s.token)
	mux.HandleFunc("POST "+RevokePath, s.revoke)
	mux.HandleFunc("OPTIONS /oauth/", preflight)
}

// cors lets a browser-based client read the metadata and use the token endpoint: they hold nothing
// a page could not read anyway, and no cookie is ever used here.
func cors(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
}

func preflight(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	cors(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthError is an error as RFC 6749 section 5.2 writes it.
func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// ---- metadata

// Resources are the resource identifiers a token may be asked for: /mcp, and Beacon as a whole.
func (s *Server) Resources(r *http.Request) []string {
	iss := s.Issuer(r)
	return []string{iss + MCPPath, iss, iss + "/"}
}

func (s *Server) resourceMetadata(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iss := s.Issuer(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"resource":                 iss + path,
			"authorization_servers":    []string{iss},
			"bearer_methods_supported": []string{"header"},
			"resource_name":            "Beacon",
			"resource_documentation":   documentationURL,
		})
	}
}

// Scopes are the scopes a client may ask for: an agent's presets, and offline_access for a refresh
// token.
func Scopes() []string { return append(slices.Clone(store.Presets), OfflineAccess) }

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	iss := s.Issuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         iss,
		"authorization_endpoint":                         iss + AuthorizePath,
		"token_endpoint":                                 iss + TokenPath,
		"registration_endpoint":                          iss + RegisterPath,
		"revocation_endpoint":                            iss + RevokePath,
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"scopes_supported":                               Scopes(),
		"client_id_metadata_document_supported":          !s.NoDocuments,
		"authorization_response_iss_parameter_supported": true,
		"service_documentation":                          documentationURL,
	})
}

// ---- redirect URIs

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// CheckRedirect says what is wrong with a redirect URI a client gives, or nil: an http URL on the
// loopback interface (127.0.0.1, localhost, [::1], any port), or an https URL; no fragment, no
// credentials.
func CheckRedirect(raw string) error {
	if len(raw) > maxURL {
		return errors.New("a redirect URI is at most 2000 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return fmt.Errorf("redirect URI %q is not an absolute URL", raw)
	}
	switch {
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return fmt.Errorf("redirect URI %q has a fragment", raw)
	case u.User != nil:
		return fmt.Errorf("redirect URI %q has credentials", raw)
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && loopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("redirect URI %q: only http://127.0.0.1, http://localhost and https URLs are allowed", raw)
}

// redirectMatches reports whether got is one of a client's redirect URIs: the same, or the same
// loopback URI on another port (RFC 8252 section 7.3: a native client listens on whatever port is free).
func redirectMatches(registered []string, got string) bool {
	if CheckRedirect(got) != nil {
		return false
	}
	g, _ := url.Parse(got)
	for _, r := range registered {
		if r == got {
			return true
		}
		p, err := url.Parse(r)
		if err == nil && p.Scheme == "http" && g.Scheme == "http" && loopback(p.Hostname()) && p.Hostname() == g.Hostname() &&
			p.EscapedPath() == g.EscapedPath() && p.RawQuery == g.RawQuery {
			return true
		}
	}
	return false
}

// cleanName makes a client's name safe to show: one line of at most 64 characters.
func cleanName(name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name))
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	return name
}

// ---- dynamic client registration

func (s *Server) client(r *http.Request) netip.Addr {
	if s.Auth != nil {
		return s.Auth.Client(r)
	}
	return netip.Addr{}
}

// allowRegister counts a registration from the request's address: registerLimit in registerWindow.
func (s *Server) allowRegister(r *http.Request) bool {
	ip := s.client(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.regs == nil || len(s.regs) > 10000 {
		s.regs = map[netip.Addr]*window{}
	}
	w := s.regs[ip]
	if w == nil || s.now().Sub(w.start) >= registerWindow {
		w = &window{start: s.now()}
		s.regs[ip] = w
	}
	w.n++
	return w.n <= registerLimit
}

// register is RFC 7591 for public clients: redirect URIs on the loopback interface or https, the
// authorization code grant with refresh tokens, no client secret.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.allowRegister(r) {
		oauthError(w, http.StatusTooManyRequests, "too_many_requests", "too many registrations from here; try again in a few minutes")
		return
	}
	var in struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		ClientURI               string   `json:"client_uri"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		Scope                   string   `json:"scope"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&in); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is not a JSON client registration")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > maxRedirectURIs {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give 1 to 10 redirect_uris")
		return
	}
	for _, u := range in.RedirectURIs {
		if err := CheckRedirect(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	if m := in.TokenEndpointAuthMethod; m != "" && m != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "Beacon registers public clients only: token_endpoint_auth_method none, with PKCE")
		return
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "grant_types are authorization_code and refresh_token")
			return
		}
	}
	for _, t := range in.ResponseTypes {
		if t != "code" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "response_types is code")
			return
		}
	}
	name := cleanName(in.ClientName)
	if name == "" {
		name = "MCP client"
	}
	clientURI := ""
	if u, err := url.Parse(in.ClientURI); err == nil && u.Scheme == "https" && len(in.ClientURI) <= maxURL {
		clientURI = in.ClientURI
	}
	c, err := s.Store.RegisterClient(store.OAuthClient{Name: name, RedirectURIs: in.RedirectURIs, URI: clientURI})
	if errors.Is(err, store.ErrTooMany) {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", err.Error())
		return
	}
	if err != nil {
		s.log().Error("oauth registration failed", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "the registration could not be stored")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        c.Created.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

// ---- authorization requests

// Request is an authorization request that passed its checks: the client, where to send the answer,
// the PKCE challenge, the scopes asked for and the resource.
type Request struct {
	ClientID    string
	ClientName  string
	ClientURI   string
	RedirectURI string
	State       string
	Challenge   string
	// RedirectGiven says the request named its redirect URI (one left out is the client's only one, and
	// may be left out of the token request too).
	RedirectGiven bool
	Scopes        []string
	Resource      string
	Offline       bool
}

// Presets are the presets the request asks for (none: let the person choose).
func (q Request) Presets() []string {
	var out []string
	for _, sc := range q.Scopes {
		if store.ValidPreset(sc) {
			out = append(out, sc)
		}
	}
	return out
}

// Problem is an authorization request that failed its checks. Redirect is where to tell the client
// (with Code, Description and the request's State), once the client and its redirect URI are known
// good; otherwise the person is shown Description and nothing is sent anywhere.
type Problem struct {
	Code, Description, State, Redirect string
}

func (p *Problem) Error() string { return p.Code + ": " + p.Description }

// RedirectURL is the client's redirect URI with the error.
func (p *Problem) RedirectURL(issuer string) string {
	return withParams(p.Redirect, url.Values{"error": {p.Code}, "error_description": {p.Description}, "state": {p.State}, "iss": {issuer}})
}

func withParams(redirect string, v url.Values) string {
	u, _ := url.Parse(redirect)
	q := u.Query()
	for k, vals := range v {
		if len(vals) > 0 && vals[0] != "" {
			q.Set(k, vals[0])
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// resolveClient finds a client by its ID: a registration, or the client ID metadata document at an
// https URL.
func (s *Server) resolveClient(ctx context.Context, id string) (store.OAuthClient, error) {
	if strings.HasPrefix(id, "https://") {
		if s.NoDocuments {
			return store.OAuthClient{}, errors.New("this Beacon takes no client ID metadata documents (oauth.client_id_documents is false); the client registers itself")
		}
		return s.document(ctx, id)
	}
	c, ok := s.Store.OAuthClient(id)
	if !ok {
		return store.OAuthClient{}, errors.New("unknown client_id; register the client first")
	}
	return c, nil
}

var challengeOK = func(c string) bool {
	if len(c) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil
}

// Parse checks an authorization request (its query, or the consent form's fields, which carry it on).
func (s *Server) Parse(r *http.Request, v url.Values) (Request, *Problem) {
	q := Request{ClientID: v.Get("client_id"), RedirectURI: v.Get("redirect_uri"), State: v.Get("state"),
		Challenge: v.Get("code_challenge"), Resource: v.Get("resource")}
	if q.ClientID == "" || len(q.ClientID) > maxURL {
		return q, &Problem{Code: "invalid_request", Description: "client_id is missing"}
	}
	c, err := s.resolveClient(r.Context(), q.ClientID)
	if err != nil {
		return q, &Problem{Code: "invalid_client", Description: "The client is unknown to Beacon: " + err.Error()}
	}
	q.ClientName, q.ClientURI = c.Name, c.URI
	q.RedirectGiven = q.RedirectURI != ""
	if q.RedirectURI == "" && len(c.RedirectURIs) == 1 {
		q.RedirectURI = c.RedirectURIs[0]
	}
	if !redirectMatches(c.RedirectURIs, q.RedirectURI) {
		return q, &Problem{Code: "invalid_request", Description: "redirect_uri is not one the client registered"}
	}
	bad := func(code, desc string) (Request, *Problem) {
		return q, &Problem{Code: code, Description: desc, State: q.State, Redirect: q.RedirectURI}
	}
	switch {
	case v.Get("response_type") != "code":
		return bad("unsupported_response_type", "response_type must be code")
	case q.Challenge == "":
		return bad("invalid_request", "PKCE is required: send code_challenge with code_challenge_method S256")
	case v.Get("code_challenge_method") != "S256":
		return bad("invalid_request", "code_challenge_method must be S256")
	case !challengeOK(q.Challenge):
		return bad("invalid_request", "code_challenge is not a base64url SHA-256")
	case len(q.State) > 1000:
		return bad("invalid_request", "state is too long")
	}
	if q.Resource != "" && !slices.Contains(s.Resources(r), q.Resource) {
		return bad("invalid_target", "resource must be "+s.Issuer(r)+MCPPath)
	}
	for _, sc := range strings.Fields(v.Get("scope")) {
		if !slices.Contains(Scopes(), sc) {
			continue // a scope Beacon does not have asks for nothing more; the person chooses on the consent page
		}
		if !slices.Contains(q.Scopes, sc) {
			q.Scopes = append(q.Scopes, sc)
		}
	}
	q.Offline = slices.Contains(q.Scopes, OfflineAccess)
	return q, nil
}

// Fields are the request's parameters, for the consent form to carry on.
func (q Request) Fields() url.Values {
	v := url.Values{"response_type": {"code"}, "client_id": {q.ClientID},
		"code_challenge": {q.Challenge}, "code_challenge_method": {"S256"}, "scope": {strings.Join(q.Scopes, " ")}}
	if q.RedirectGiven {
		v.Set("redirect_uri", q.RedirectURI)
	}
	if q.State != "" {
		v.Set("state", q.State)
	}
	if q.Resource != "" {
		v.Set("resource", q.Resource)
	}
	return v
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte("oauth-code:" + code))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Code issues the authorization code for an allowed request, for agent with preset and machines (none:
// the agent's) until the given time, and returns where to send the person: the client's redirect URI with the code, the state and
// the issuer (RFC 9207).
func (s *Server) Code(r *http.Request, q Request, agent, preset string, machines []string, until time.Time) (string, error) {
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	now := s.now()
	s.mu.Lock()
	if s.codes == nil {
		s.codes = map[string]*pending{}
	}
	for h, p := range s.codes {
		if !now.Before(p.Expires) {
			delete(s.codes, h)
		}
	}
	if len(s.codes) >= maxPendingCodes {
		s.mu.Unlock()
		return "", errors.New("too many sign-ins waiting; try again in a minute")
	}
	s.codes[hashCode(code)] = &pending{Request: q, Agent: agent, Preset: preset, Machines: machines, Until: until, Expires: now.Add(codeTTL)}
	s.mu.Unlock()
	if !strings.HasPrefix(q.ClientID, "https://") {
		s.Store.UseClient(q.ClientID)
	}
	return withParams(q.RedirectURI, url.Values{"code": {code}, "state": {q.State}, "iss": {s.Issuer(r)}}), nil
}

// Denied is where to send the person who said no.
func (s *Server) Denied(r *http.Request, q Request) string {
	p := &Problem{Code: "access_denied", Description: "the person did not allow it", State: q.State, Redirect: q.RedirectURI}
	return p.RedirectURL(s.Issuer(r))
}

func randomCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---- the token endpoint

// verifierOK checks a PKCE code verifier's form (RFC 7636 section 4.1).
func verifierOK(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !unreserved(c) {
			return false
		}
	}
	return true
}

// unreserved reports whether c may be in a code verifier: a letter, a digit, or one of -._~.
func unreserved(c rune) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)
}

// clientID is the client a token or revocation request names: in the form, or as the user of HTTP
// Basic authentication (with an empty secret: Beacon's clients are public).
func clientID(r *http.Request) string {
	if id := r.PostForm.Get("client_id"); id != "" {
		return id
	}
	if u, _, ok := r.BasicAuth(); ok {
		if id, err := url.QueryUnescape(u); err == nil {
			return id
		}
	}
	return ""
}

func (s *Server) blocked(r *http.Request) bool { return s.Auth != nil && s.Auth.Blocked(r) }

func (s *Server) fail(r *http.Request) {
	if s.Auth != nil {
		s.Auth.Fail(r)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if s.blocked(r) {
		oauthError(w, http.StatusTooManyRequests, "too_many_requests", "too many failed attempts from here; try again later")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send the request as application/x-www-form-urlencoded")
		return
	}
	id := clientID(r)
	if id == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client_id is missing")
		return
	}
	if res := r.PostForm.Get("resource"); res != "" && !slices.Contains(s.Resources(r), res) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource must be "+s.Issuer(r)+MCPPath)
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchange(w, r, id)
	case "refresh_token":
		s.refresh(w, r, id)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type is authorization_code or refresh_token")
	}
}

func issued(w http.ResponseWriter, in store.Issued, now time.Time) {
	out := map[string]any{"access_token": in.Access, "token_type": "Bearer", "expires_in": int(in.AccessExpires.Sub(now).Seconds()), "scope": in.Grant.Scope}
	if in.Refresh != "" {
		out["refresh_token"] = in.Refresh
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request, id string) {
	code, verifier, redirect := r.PostForm.Get("code"), r.PostForm.Get("code_verifier"), r.PostForm.Get("redirect_uri")
	h := hashCode(code)
	now := s.now()
	s.mu.Lock()
	p := s.codes[h]
	delete(s.codes, h) // one-time, whatever comes next
	if s.spent == nil {
		s.spent = map[string]time.Time{}
	}
	_, replay := s.spent[h]
	if p != nil {
		s.spent[h] = now
	}
	for k, at := range s.spent {
		if now.Sub(at) > spentTTL {
			delete(s.spent, k)
		}
	}
	s.mu.Unlock()
	if replay {
		if g, ok := s.Store.RevokeCode(h); ok && s.Bridge != nil {
			s.Bridge.OAuthReplayed(r.Context(), g.Agent, g.ClientName, "code")
		}
		s.log().Warn("oauth: an authorization code was used again; its grant is revoked")
		s.fail(r)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was used already")
		return
	}
	switch {
	case p == nil || !now.Before(p.Expires):
		s.fail(r)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code is unknown or expired; codes last 60 seconds and work once")
		return
	case p.ClientID != id:
		s.fail(r)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
		return
	case redirect != p.RedirectURI && (redirect != "" || p.RedirectGiven):
		s.fail(r)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri differs from the authorization request's")
		return
	case !verifierOK(verifier) || pkce(verifier) != p.Challenge:
		s.fail(r)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	scope := p.Preset
	if p.Offline {
		scope += " " + OfflineAccess
	}
	// Every grant gets a refresh token, valid as long as the person allowed on the consent page: MCP
	// clients refresh by themselves, and Claude Code does not ask for offline_access.
	in, err := s.Store.CreateGrant(store.Grant{Agent: p.Agent, Client: p.ClientID, ClientName: p.ClientName, Scope: scope, Preset: p.Preset, Machines: p.Machines, Expires: p.Until, Code: h}, true)
	if err != nil {
		s.log().Error("oauth: issuing tokens failed", "err", err)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the agent this code was for is gone")
		return
	}
	issued(w, in, now)
}

func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request, id string) {
	in, err := s.Store.RefreshGrant(r.PostForm.Get("refresh_token"), id)
	switch {
	case errors.Is(err, store.ErrReplayed):
		s.log().Warn("oauth: a replaced refresh token was used again; its grant is revoked", "client", id)
		if s.Bridge != nil {
			s.Bridge.OAuthReplayed(r.Context(), in.Grant.Agent, in.Grant.ClientName, "refresh_token")
		}
		s.fail(r)
		oauthError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		return
	case err != nil:
		s.fail(r)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is unknown, expired or revoked; sign in again")
		return
	}
	issued(w, in, s.now())
}

// revoke is RFC 7009: the answer is 200 whether or not the token was known, so that it tells nothing.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("token") == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send the token to revoke as application/x-www-form-urlencoded")
		return
	}
	id := clientID(r)
	if id == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client_id is missing")
		return
	}
	if g, ok := s.Store.RevokeOAuthToken(r.PostForm.Get("token"), id); ok {
		_ = s.Store.Audit(store.AuditEntry{Actor: g.ClientName, Via: "oauth", Action: "oauth_token_revoke", Target: g.Agent})
		if s.Bridge != nil {
			s.Bridge.Recheck() // a refresh token ends its grant, and the sessions made with it
		}
	}
	cors(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// hostOf is the host of a URL, for the consent page's Content-Security-Policy.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// FormAction is the source the consent page's form may lead to: the client's redirect URI's origin.
func (q Request) FormAction() string { return hostOf(q.RedirectURI) }
