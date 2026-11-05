package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
)

// The console's sign-in. People sign in with their Beacon account, a username and a password (no
// external sign-in of any kind), and get a session bound to that account, kept in the store for 12
// hours; what the console may do is what the account's role allows, read afresh on every request, and
// disabling the account ends its sessions. For one release a person may still sign in with a Beacon
// token instead (the owner before setup, scripts): the session is bound to that token, ends when it
// expires, is revoked or replaced, and may do what the token may.
//
// The cookie holds only the session's id, HttpOnly, Secure and SameSite=Strict; every form carries the
// session's CSRF secret, and the forms that sign in carry a secret of their own. Failed sign-ins count
// against the same limits as the API's, per address and, for passwords, per username.

const (
	// sessionTTL is how long a sign-in lasts.
	sessionTTL = 12 * time.Hour
	// sessionCookie holds the session's id. __Host- keeps it to this host, Secure and on /.
	sessionCookie = "__Host-beacon-console"
	// signInCookie holds the sign-in forms' own secret, so that no other site can sign a person in with
	// an account or a token of its choosing.
	signInCookie = "__Host-beacon-sign-in"
	// csrfField is the form field that carries the CSRF secret.
	csrfField = "csrf"
	// maxForm bounds a form's body.
	maxForm = 64 << 10
)

// viewer is who is signed in to the console: the session, and the account (Account) or the token
// (Token) it is bound to, resolved to the principal it is now.
type viewer struct {
	ID        string
	Session   store.ConsoleSession
	Principal store.Principal
	Account   *store.Account
	Token     *store.Token
}

// has reports whether the viewer may use scope now.
func (v *viewer) has(scope string) bool { return v != nil && v.Principal.Has(scope) }

// hasAny reports whether the viewer may use one of the scopes ("a|b").
func (v *viewer) hasAny(scopes string) bool {
	return v != nil && slices.ContainsFunc(strings.Split(scopes, "|"), v.Principal.Has)
}

// person reports whether the viewer signed in with an account.
func (v *viewer) person() bool { return v != nil && v.Account != nil }

// owner reports whether the viewer manages people and everyone's agents.
func (v *viewer) owner() bool { return v.has("accounts:write") }

// name is how the audit log names the viewer.
func (v *viewer) name() string { return v.Principal.Name() }

// actor is who the viewer is to the bridge.
func (v *viewer) actor() bridge.Actor { return bridge.ActorOf(v.Principal, "console") }

// gate is who the viewer is to the SSH channel.
func (v *viewer) gate() sshgate.Actor {
	return sshgate.Actor{Name: v.name(), Via: "console", Key: v.Principal.Key(), Cred: v.Principal.Cred()}
}

// viewer returns who is signed in, or nil. A session whose account was disabled, or whose token was
// revoked, replaced or has expired, is ended on the way.
func (s *Server) viewer(r *http.Request) *viewer {
	if !s.Console() {
		return nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	cs, err := s.Store.ConsoleSessionByID(c.Value)
	if err != nil {
		return nil
	}
	if cs.User != "" {
		a, ok := s.Store.Account(cs.User)
		p, live := s.Store.AccountPrincipal(cs.User)
		if ok && live && p.Has("status:read") {
			return &viewer{ID: c.Value, Session: cs, Principal: p, Account: &a}
		}
	} else {
		for _, t := range s.Store.Tokens() { // the session ends by the token's expiry at the latest (store.CreateConsoleSession)
			if t.Name == cs.Token && t.Created.Equal(cs.TokenCreated) {
				if p, ok := s.Store.TokenPrincipal(t); ok && p.Has("status:read") {
					return &viewer{ID: c.Value, Session: cs, Principal: p, Token: &t}
				}
			}
		}
	}
	_ = s.Store.DeleteConsoleSession(c.Value)
	return nil
}

func (s *Server) registerConsole(mux *http.ServeMux) {
	// Every route of the console is under /console; the alert's and the session's pages, which signed
	// links open too, are registered in Register. The cookies stay on / (__Host- asks for it).
	at := func(method, path string, h http.HandlerFunc) { mux.HandleFunc(method+" "+consolePath(path), h) }
	at("GET", "/sign-in", s.signInPage)
	at("POST", "/sign-in", s.signIn)
	at("POST", "/sign-out", s.action("", s.signOut))
	s.registerAccounts(at)

	at("GET", "", s.console("", s.overview))
	at("GET", "/{$}", s.toOverview)
	at("GET", "/alerts", s.console("", s.alerts))
	at("POST", "/alerts/{id}/ack", s.action("alerts:write", s.ack))
	at("POST", "/alerts/{id}/silence", s.action("alerts:write", s.silence))
	at("POST", "/alerts/{id}/notes", s.action("alerts:write", s.note))
	at("GET", "/machines", s.console("status:read|machines:ssh", s.machines))
	at("GET", "/machines/{name}", s.console("status:read|machines:ssh", s.machine))
	at("POST", "/machines/pause", s.action("machines:write", s.pause(true)))
	at("POST", "/machines/resume", s.action("machines:write", s.pause(false)))
	at("GET", "/sessions", s.console("status:read|machines:ssh", s.sessionsList))
	at("GET", "/tokens", s.console("", s.tokens))
	at("POST", "/tokens/{name}/revoke", s.action("machines:write", s.revoke))
	at("POST", "/tokens/{name}/claim", s.action("accounts:write", s.claim))
	at("GET", "/config", s.console("status:read|config:write", s.configPage))
	at("GET", "/settings", s.console("status:read|secrets:admin", s.settingsPage))
	if s.Vault != nil {
		at("GET", "/secrets", s.console("", s.vaultPage))
		at("GET", "/secrets/{id}", s.console("", s.vaultCollectionPage))
		at("GET", "/secrets/item/{id}", s.console("", s.vaultItemPage))
		at("POST", "/secrets/unseal", s.action("secrets:admin", s.vaultUnsealForm))
		at("POST", "/secrets/collection", s.action("secrets:admin", s.vaultCollectionForm))
		at("POST", "/secrets/{id}/perms", s.action("secrets:admin", s.vaultPermForm))
		at("POST", "/secrets/item/{id}/set", s.action("", s.vaultItemForm))
		at("POST", "/secrets/item/{id}/state", s.action("", s.vaultStateForm))
		at("POST", "/secrets/item/{id}/reveal", s.action("", s.vaultRevealForm))
	}
	at("POST", "/settings/{key}/set", s.action("secrets:admin", s.setSetting))
	at("POST", "/settings/{key}/reset", s.action("secrets:admin", s.resetSetting))
	at("POST", "/settings/history/{version}/revert", s.action("secrets:admin", s.revertSetting))
	at("GET", "/config/history/{version}", s.console("status:read|config:write", s.versionPage))
	at("POST", "/config/history/{version}/revert", s.action("config:write", s.revert))
	s.registerAgents(at)
	at("GET", "/hooks", s.console("", s.hooks))
	at("GET", "/audit", s.console("", s.audit))
	at("GET", "/changes", s.console("", s.changes))
	if s.OAuth != nil {
		mux.HandleFunc("GET /oauth/authorize", s.authorize)
		mux.HandleFunc("POST /oauth/authorize", s.consent)
	}
}

// toOverview sends /console/ to /console, where the overview is.
func (s *Server) toOverview(w http.ResponseWriter, r *http.Request) {
	headers(w)
	to := consolePath("")
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, http.StatusPermanentRedirect) //nolint:gosec // G710: a fixed path on this site
}

// toSignIn sends the person to sign in, and back to where they were.
func toSignIn(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, consolePath("/sign-in")+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
}

// console guards a page of the console: signed out, it sends the person to sign in and back; without
// the scope ("a|b"; "" for anyone signed in), it says what the page needs.
func (s *Server) console(scope string, h func(http.ResponseWriter, *http.Request, *viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		v := s.viewer(r)
		if v == nil {
			toSignIn(w, r)
			return
		}
		if scope != "" && !v.hasAny(scope) {
			s.forbidden(w, r, v, scope)
			return
		}
		h(w, r, v)
	}
}

// action guards a form the console posts: signed in, from Beacon's own page (its CSRF secret, and the
// browser's word that the request is same-origin when it gives one), with the scope the matching API
// route needs. Every write then goes through the bridge, the SSH channel or the configuration with Via
// "console", audited and echoed to the chat like the API's.
func (s *Server) action(scope string, h func(http.ResponseWriter, *http.Request, *viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		v := s.viewer(r)
		if v == nil {
			http.Redirect(w, r, consolePath("/sign-in"), http.StatusSeeOther)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxForm)
		if err := r.ParseForm(); err != nil || !sameOrigin(r) || !equal(r.PostForm.Get(csrfField), v.Session.CSRF) {
			s.message(w, r, v, http.StatusForbidden, s.t("The form has expired"), s.t("Reload the page and try again."))
			return
		}
		if scope != "" && !v.hasAny(scope) {
			s.forbidden(w, r, v, scope)
			return
		}
		h(w, r, v)
	}
}

// sameOrigin reports whether the browser says the request came from Beacon's own pages; a browser that
// says nothing is judged by the CSRF secret and the cookie's SameSite alone.
func sameOrigin(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin"
}

func equal(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// local keeps a next parameter that is a path on this site, and turns anything else into the console's
// overview. Spaces, control characters and backslashes are refused outright: browsers drop or turn
// some of them into slashes, which could make "/\t/example.com" another site.
func local(next string) string {
	odd := func(r rune) bool { return r <= ' ' || r == 0x7f || r == '\\' }
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsFunc(next, odd) {
		return consolePath("")
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return consolePath("")
	}
	return next
}

// cookie is one of the console's cookies, until expires (zero: deleted).
func (s *Server) cookie(name, value string, expires time.Time) *http.Cookie {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode}
	if expires.IsZero() {
		c.MaxAge = -1
	} else {
		c.Expires = expires.UTC()
		c.MaxAge = max(1, int(expires.Sub(s.now()).Seconds()))
	}
	return c
}

// ---- signing in and out

type signInLabels struct {
	Title, Help, Token, Submit, Hint, HowTo, Username, Password, WithToken, WithPassword, NoAccounts string
}

type signInPage struct {
	shell
	L                 signInLabels
	Next, Form, Error string
	WithToken         bool   // the token form rather than the password form
	NoAccounts        bool   // nobody has an account yet
	TokenHref         string // the token form, keeping next
	PasswordHref      string
	Username          string
}

var signInTmpl = parse("signin.html")

func (s *Server) signInLabels() signInLabels {
	return signInLabels{
		Title: s.t("Sign in to the console"), Help: s.t("Paste a Beacon token. What you may do here is what its scopes allow; status:read at least."),
		Token: s.t("Token"), Submit: s.t("Sign in"), Hint: s.t("Beacon keeps only a session bound to the token, for 12 hours or until the token expires or is revoked; the token itself is not stored."),
		HowTo: s.t("On the host, make a token for yourself with"), Username: s.t("Username"), Password: s.t("Password"),
		WithToken: s.t("Sign in with a token instead"), WithPassword: s.t("Sign in with your username and password"),
		NoAccounts: s.t("Nobody has an account yet. Beacon wrote a one-time setup link to its log when it started; beacon setup on the host prints a new one."),
	}
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	headers(w)
	next := local(r.URL.Query().Get("next"))
	if v := s.viewer(r); v != nil {
		http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next went through local, which keeps it on this site
		return
	}
	s.signInForm(w, r, http.StatusOK, next, r.URL.Query().Get("with") == "token", "", "")
}

// formSecret sets a fresh secret of the sign-in forms' own, in a cookie, and returns it for the form.
func (s *Server) formSecret(w http.ResponseWriter) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	form := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, s.cookie(signInCookie, form, s.now().Add(time.Hour)))
	return form
}

// formOK checks a sign-in form's own secret against its cookie.
func formOK(r *http.Request) bool {
	c, err := r.Cookie(signInCookie)
	return err == nil && sameOrigin(r) && equal(c.Value, r.PostForm.Get("form"))
}

// signInForm shows the password form, or the token form, with a fresh secret of its own.
func (s *Server) signInForm(w http.ResponseWriter, r *http.Request, status int, next string, withToken bool, username, problem string) {
	p := signInPage{shell: s.bare(r, s.t("Sign in")), L: s.signInLabels(), Next: next, Form: s.formSecret(w), Error: problem,
		WithToken: withToken, NoAccounts: !s.Store.HasAccounts(), Username: username,
		TokenHref:    consolePath("/sign-in") + "?with=token&next=" + url.QueryEscape(next),
		PasswordHref: consolePath("/sign-in") + "?next=" + url.QueryEscape(next)}
	if p.NoAccounts {
		p.WithToken = true
	}
	if !p.WithToken {
		p.L.Help = s.t("Sign in with your Beacon account.")
	}
	s.renderStatus(w, status, signInTmpl, p)
}

// waitMinutes rounds a lockout's wait up to whole minutes, a minute at least.
func waitMinutes(d time.Duration) int { return max(1, int((d+time.Minute-1)/time.Minute)) }

// lockedOutFromHere is the console's words for the caller's address being locked out, with the wait.
func (s *Server) lockedOutFromHere(d time.Duration) string {
	minutes := waitMinutes(d)
	if minutes <= 1 {
		return s.t("Too many failed sign-ins from here; try again in a minute.")
	}
	return s.t("Too many failed sign-ins from here; try again in %d minutes.", minutes)
}

// lockedOut is the console's words for a sign-in refused by a lockout — the address's or the
// username's, whichever waits longer.
func (s *Server) lockedOut(r *http.Request, username string) string {
	wait := time.Duration(0)
	if s.Auth != nil {
		if d, ok := s.Auth.BlockedFor(r); ok {
			wait = d
		}
		if d, ok := s.Auth.UserBlockedFor(username); ok && d > wait {
			wait = d
		}
	}
	if wait == 0 { // the window ended while the request was being answered
		return s.t("Too many failed sign-ins; try again in a few minutes.")
	}
	minutes := waitMinutes(wait)
	if minutes <= 1 {
		return s.t("Too many failed sign-ins; try again in a minute.")
	}
	return s.t("Too many failed sign-ins; try again in %d minutes.", minutes)
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	headers(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		s.signInForm(w, r, http.StatusBadRequest, consolePath(""), false, "", s.t("The form has expired"))
		return
	}
	next := local(r.PostForm.Get("next"))
	_, withToken := r.PostForm["token"]
	if !formOK(r) {
		s.signInForm(w, r, http.StatusForbidden, next, withToken, "", s.t("The form has expired; try again."))
		return
	}
	if s.Auth == nil {
		s.signInForm(w, r, http.StatusTooManyRequests, next, withToken, "", s.t("Too many failed sign-ins from here; try again in a few minutes."))
		return
	}
	if d, blocked := s.Auth.BlockedFor(r); blocked {
		s.signInForm(w, r, http.StatusTooManyRequests, next, withToken, "", s.lockedOutFromHere(d))
		return
	}
	if withToken {
		s.signInWithToken(w, r, next)
		return
	}
	username := strings.ToLower(strings.TrimSpace(r.PostForm.Get("username")))
	a, err := s.Auth.SignIn(r, username, r.PostForm.Get("password"))
	switch {
	case errors.Is(err, api.ErrBlocked):
		s.signInForm(w, r, http.StatusTooManyRequests, next, false, username, s.lockedOut(r, username))
		return
	case errors.Is(err, store.ErrDisabled):
		s.signInForm(w, r, http.StatusForbidden, next, false, username, s.t("This account is disabled; an owner can enable it."))
		return
	case err != nil:
		s.signInForm(w, r, http.StatusUnauthorized, next, false, username, s.t("Wrong username or password."))
		return
	}
	s.startSession(w, r, a, next)
}

// startSession signs an account in and sends the person on.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, a store.Account, next string) {
	id, cs, err := s.Store.CreateAccountSession(a, sessionTTL)
	if err != nil {
		s.log().Error("console sign-in failed", "err", err)
		s.signInForm(w, r, http.StatusInternalServerError, next, false, a.Username, s.t("Signing in failed; try again."))
		return
	}
	_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: a.Username, Via: "console", Action: "console_sign_in", Detail: "until " + cs.Expires.UTC().Format(time.RFC3339)})
	http.SetCookie(w, s.cookie(signInCookie, "", time.Time{}))
	http.SetCookie(w, s.cookie(sessionCookie, id, cs.Expires))
	http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next went through local, which keeps it on this site
}

func (s *Server) signInWithToken(w http.ResponseWriter, r *http.Request, next string) {
	secret := strings.TrimSpace(r.PostForm.Get("token"))
	t, ok := s.Auth.Verify(r, secret)
	if !ok {
		s.signInForm(w, r, http.StatusUnauthorized, next, true, "", s.t("The token is unknown or has expired."))
		return
	}
	p, ok := s.Store.TokenPrincipal(t)
	if !ok || !p.Has("status:read") {
		s.signInForm(w, r, http.StatusForbidden, next, true, "", s.t("Token %s lacks status:read, which the console needs to show anything.", t.Name))
		return
	}
	id, cs, err := s.Store.CreateConsoleSession(t, sessionTTL)
	if err != nil {
		s.log().Error("console sign-in failed", "err", err)
		s.signInForm(w, r, http.StatusInternalServerError, next, true, "", s.t("Signing in failed; try again."))
		return
	}
	_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: p.Name(), Via: "console", Action: "console_sign_in", Detail: "with a token, until " + cs.Expires.UTC().Format(time.RFC3339)})
	http.SetCookie(w, s.cookie(signInCookie, "", time.Time{}))
	http.SetCookie(w, s.cookie(sessionCookie, id, cs.Expires))
	http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next went through local, which keeps it on this site
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request, v *viewer) {
	_ = s.Store.DeleteConsoleSession(v.ID)
	http.SetCookie(w, s.cookie(sessionCookie, "", time.Time{}))
	http.Redirect(w, r, consolePath("/sign-in"), http.StatusSeeOther)
}
