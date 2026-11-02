package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// The console's sign-in. Beacon has no accounts and no passwords: a person pastes a Beacon token (one
// with status:read at least), and gets a session bound to that token, kept in the store for 12 hours
// or until the token expires. Revoking the token, or replacing it by another of the same name, ends
// the session; what the console may do is what the token's scopes allow, read afresh on every request.
// The cookie holds only the session's id, HttpOnly, Secure and SameSite=Strict; every form carries the
// session's CSRF secret. Failed sign-ins count against the same limit as the API's.

const (
	// sessionTTL is how long a sign-in lasts.
	sessionTTL = 12 * time.Hour
	// sessionCookie holds the session's id. __Host- keeps it to this host, Secure and on /.
	sessionCookie = "__Host-beacon-console"
	// signInCookie holds the sign-in form's own secret, so that no other site can sign a person in
	// with a token of its choosing.
	signInCookie = "__Host-beacon-sign-in"
	// csrfField is the form field that carries the CSRF secret.
	csrfField = "csrf"
	// maxForm bounds a form's body.
	maxForm = 64 << 10
)

// viewer is the person signed in to the console: the session and the token it is bound to.
type viewer struct {
	ID      string
	Session store.ConsoleSession
	Token   store.Token
}

// has reports whether the viewer's token carries scope.
func (v *viewer) has(scope string) bool { return v != nil && v.Token.Has(scope) }

// hasAny reports whether the viewer's token carries one of the scopes ("a|b").
func (v *viewer) hasAny(scopes string) bool {
	return v != nil && slices.ContainsFunc(strings.Split(scopes, "|"), v.Token.Has)
}

// actor is who the viewer is to the bridge.
func (v *viewer) actor() bridge.Actor { return bridge.Actor{Name: v.Token.Name, Via: "console"} }

// viewer returns who is signed in, or nil. A session whose token was revoked, replaced or has expired
// is ended on the way.
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
	for _, t := range s.Store.Tokens() { // the session ends by the token's expiry at the latest (store.CreateConsoleSession)
		if t.Name == cs.Token && t.Created.Equal(cs.TokenCreated) && t.Has("status:read") {
			return &viewer{ID: c.Value, Session: cs, Token: t}
		}
	}
	_ = s.Store.DeleteConsoleSession(c.Value)
	return nil
}

func (s *Server) registerConsole(mux *http.ServeMux) {
	mux.HandleFunc("GET /sign-in", s.signInPage)
	mux.HandleFunc("POST /sign-in", s.signIn)
	mux.HandleFunc("POST /sign-out", s.action("", s.signOut))

	mux.HandleFunc("GET /{$}", s.console("", s.overview))
	mux.HandleFunc("GET /alerts", s.console("", s.alerts))
	mux.HandleFunc("POST /alerts/{id}/ack", s.action("alerts:write", s.ack))
	mux.HandleFunc("POST /alerts/{id}/silence", s.action("alerts:write", s.silence))
	mux.HandleFunc("POST /alerts/{id}/notes", s.action("alerts:write", s.note))
	mux.HandleFunc("GET /machines", s.console("status:read|machines:ssh", s.machines))
	mux.HandleFunc("GET /machines/{name}", s.console("status:read|machines:ssh", s.machine))
	mux.HandleFunc("POST /machines/pause", s.action("machines:write", s.pause(true)))
	mux.HandleFunc("POST /machines/resume", s.action("machines:write", s.pause(false)))
	mux.HandleFunc("GET /sessions", s.console("status:read|machines:ssh", s.sessionsList))
	mux.HandleFunc("GET /tokens", s.console("", s.tokens))
	mux.HandleFunc("POST /tokens/{name}/revoke", s.action("machines:write", s.revoke))
	mux.HandleFunc("GET /config", s.console("status:read|config:write", s.configPage))
	mux.HandleFunc("GET /config/history/{version}", s.console("status:read|config:write", s.versionPage))
	mux.HandleFunc("POST /config/history/{version}/revert", s.action("config:write", s.revert))
	mux.HandleFunc("GET /agents", s.console("", s.agents))
	mux.HandleFunc("GET /audit", s.console("", s.audit))
	mux.HandleFunc("GET /changes", s.console("", s.changes))
}

// console guards a page of the console: signed out, it sends the person to sign in and back; without
// the scope ("a|b"; "" for any signed-in token), it says which scope the page needs.
func (s *Server) console(scope string, h func(http.ResponseWriter, *http.Request, *viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		v := s.viewer(r)
		if v == nil {
			http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
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
			http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
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

// local keeps a next parameter that is a path on this site, and turns anything else into "/".
func local(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
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

type signInLabels struct{ Title, Help, Token, Submit, Hint, HowTo string }

type signInPage struct {
	shell
	L                 signInLabels
	Next, Form, Error string
}

var signInTmpl = parse("signin.html")

func (s *Server) signInLabels() signInLabels {
	return signInLabels{
		Title: s.t("Sign in to the console"), Help: s.t("Paste a Beacon token. What you may do here is what its scopes allow; status:read at least."),
		Token: s.t("Token"), Submit: s.t("Sign in"), Hint: s.t("Beacon keeps only a session bound to the token, for 12 hours or until the token expires or is revoked; the token itself is not stored."),
		HowTo: s.t("On the host, make a token for yourself with"),
	}
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	headers(w)
	next := local(r.URL.Query().Get("next"))
	if v := s.viewer(r); v != nil {
		http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next went through local, which keeps it on this site
		return
	}
	s.signInForm(w, r, http.StatusOK, next, "")
}

// signInForm shows the form with a fresh secret of its own, in a cookie and in the form.
func (s *Server) signInForm(w http.ResponseWriter, r *http.Request, status int, next, problem string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	form := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, s.cookie(signInCookie, form, s.now().Add(time.Hour)))
	p := signInPage{shell: s.bare(r, s.t("Sign in")), L: s.signInLabels(), Next: next, Form: form, Error: problem}
	s.renderStatus(w, status, signInTmpl, p)
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	headers(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		s.signInForm(w, r, http.StatusBadRequest, "/", s.t("The form has expired"))
		return
	}
	next := local(r.PostForm.Get("next"))
	c, err := r.Cookie(signInCookie)
	if err != nil || !sameOrigin(r) || !equal(c.Value, r.PostForm.Get("form")) {
		s.signInForm(w, r, http.StatusForbidden, next, s.t("The form has expired; paste the token again."))
		return
	}
	if s.Auth == nil || s.Auth.Blocked(r) {
		s.signInForm(w, r, http.StatusTooManyRequests, next, s.t("Too many failed sign-ins from here; try again in a few minutes."))
		return
	}
	t, ok := s.Auth.Verify(r, strings.TrimSpace(r.PostForm.Get("token")))
	if !ok {
		s.signInForm(w, r, http.StatusUnauthorized, next, s.t("The token is unknown or has expired."))
		return
	}
	if !t.Has("status:read") {
		s.signInForm(w, r, http.StatusForbidden, next, s.t("Token %s lacks status:read, which the console needs to show anything.", t.Name))
		return
	}
	id, cs, err := s.Store.CreateConsoleSession(t, sessionTTL)
	if err != nil {
		s.log().Error("console sign-in failed", "err", err)
		s.signInForm(w, r, http.StatusInternalServerError, next, s.t("Signing in failed; try again."))
		return
	}
	_ = s.Store.Audit(store.AuditEntry{Time: s.now(), Actor: t.Name, Via: "console", Action: "console_sign_in", Detail: "until " + cs.Expires.UTC().Format(time.RFC3339)})
	http.SetCookie(w, s.cookie(signInCookie, "", time.Time{}))
	http.SetCookie(w, s.cookie(sessionCookie, id, cs.Expires))
	http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next went through local, which keeps it on this site
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request, v *viewer) {
	_ = s.Store.DeleteConsoleSession(v.ID)
	http.SetCookie(w, s.cookie(sessionCookie, "", time.Time{}))
	http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
}
