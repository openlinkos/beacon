// Package web serves what people look at: the console, Beacon's own workbench, and the pages people
// open from the chat (an alert's page, with its timeline and the sessions on its machines, and a
// session's page, with its replay). They are HTML from embedded templates on the design system
// @openlinkos/ui (vendored under static/ol), without a build step or a framework, served by
// beacon beside the API on the same port, in the configured language, readable on a phone.
//
// There is no login system of Beacon's own. The console signs a person in with a Beacon token they
// paste (console.go): the token is the identity, and its scopes are what the console may do. An
// alert's or a session's page also opens without signing in, view-only, with a signed link (package
// link), which the card, the chat echoes and the API hand out, or with a bearer token carrying the
// scope the matching API route needs; anything else is 404, so that a wrong or expired link reveals
// nothing, not even that the page exists. Text from outside Beacon reaches the pages cleaned by the
// bridge and is escaped by html/template on the way out; the pages carry no inline script or style,
// and their Content-Security-Policy allows nothing from elsewhere.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

//go:embed templates/*.html
var templates embed.FS

//go:embed static
var static embed.FS

// parse makes a page's template: the shell, the shared parts, then the page's own file.
func parse(page string) *template.Template {
	return template.Must(template.New("").Funcs(funcs).ParseFS(templates, "templates/shell.html", "templates/parts.html", "templates/"+page))
}

var (
	alertTmpl   = parse("alert.html")
	sessionTmpl = parse("session.html")
)

// CSP is the pages' Content-Security-Policy: scripts, styles and fonts from Beacon itself only (the
// player's terminal runs as WebAssembly, hence wasm-unsafe-eval), no inline script or style, fetches
// and forms to Beacon only.
const CSP = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; font-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// tokenTTL is how long the links inside a page last (to sessions, to the recording the player fetches)
// when the page was opened with a token rather than a signed link; opened with a link, they last as long
// as it does, and in the console as long as the sign-in.
const tokenTTL = time.Hour

// Server serves the pages. SSH is nil when the channel is off (session pages are then 404); Auth checks
// bearer tokens (nil: only signed links open pages); Pages checks the links' signatures and signs the
// links inside pages (nil: only tokens open pages, and pages link nowhere).
//
// The console needs Store, where its sign-ins are kept: nil leaves it out, and only the pages opened
// from the chat are served. Config is the configuration Beacon holds (nil: the console's configuration
// page is empty), Hooks the agent hooks configured, Reporting when each probe location last wrote
// through the gateway (nil without a gateway), and Version Beacon's own.
type Server struct {
	Bridge   *bridge.Bridge
	SSH      *sshgate.Gate
	Auth     *api.Auth
	Pages    *link.Pages
	Lang     i18n.Lang
	Location *time.Location
	Log      *slog.Logger
	Now      func() time.Time

	Store     *store.Store
	Config    *configdb.Service
	Hooks     []config.Agent
	Reporting func() map[string]time.Time
	Version   string

	// Status computes the public status pages (status.go); nil leaves them out.
	Status *status.Service
}

// Register adds the pages' routes to mux; with the console, GET / is its overview.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /alerts/{id}", s.alert)
	mux.HandleFunc("GET /sessions/{id}", s.session)
	mux.HandleFunc("GET /sessions/{id}/recording", s.recording)
	mux.HandleFunc("GET /static/{file}", s.static)
	mux.HandleFunc("GET /static/ol/{file}", s.static)
	mux.HandleFunc("GET /static/ol/fonts/{file}", s.static)
	s.registerStatus(mux)
	if s.Console() {
		s.registerConsole(mux)
	}
}

// Console reports whether the console is served (it needs the store for its sign-ins).
func (s *Server) Console() bool { return s != nil && s.Store != nil && s.Bridge != nil }

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

// t renders a message in the pages' language.
func (s *Server) t(format string, args ...any) string { return i18n.T(s.Lang, format, args...) }

// headers are set on every answer, pages and 404s alike.
func headers(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

// notFound is the one answer for a page that does not exist, a link that does not verify and a token
// that does not do: none of them learns anything.
func notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "404 page not found", http.StatusNotFound)
}

// access is how a request may see a page: signed in to the console (Viewer), through a signed link or a
// token (Token names it). Until is how long the links inside the page last: as long as the sign-in or
// the link that opened it, or tokenTTL.
type access struct {
	OK     bool
	Until  time.Time
	Token  string
	Viewer *viewer
}

// access checks the request against the console's sign-in, then the signed link it carries, then a
// bearer token, each with one of the scopes ("a|b"). Signed in without the scope, the viewer is still
// returned (OK false), so that the page can say so rather than pretend it does not exist.
func (s *Server) access(r *http.Request, scopes string) access {
	v := s.viewer(r)
	if v != nil && v.hasAny(scopes) {
		return access{OK: true, Until: v.Session.Expires, Viewer: v}
	}
	if sig := r.URL.Query().Get(link.Param); sig != "" && s.Pages != nil && s.Pages.Signer != nil {
		if until, ok := s.Pages.Signer.Verify(http.MethodGet, r.URL.Path, sig); ok {
			return access{OK: true, Until: until, Viewer: v}
		}
	}
	if secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.Auth != nil {
		if t, ok := s.Auth.Verify(r, strings.TrimSpace(secret)); ok && slices.ContainsFunc(strings.Split(scopes, "|"), t.Has) {
			return access{OK: true, Token: t.Name, Until: s.now().Add(tokenTTL)}
		}
	}
	return access{Viewer: v}
}

// signed returns path with a signature that lasts until the given time; without a signer, the path alone
// (then only a token or a sign-in opens it).
func (s *Server) signed(path string, until time.Time) string {
	if s.Pages == nil || s.Pages.Signer == nil {
		return path
	}
	return path + "?" + link.Param + "=" + s.Pages.Signer.SignUntil(http.MethodGet, path, until)
}

func (s *Server) render(w http.ResponseWriter, t *template.Template, data any) {
	s.renderStatus(w, http.StatusOK, t, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, status int, t *template.Template, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.log().Error("page template failed", "err", err)
		http.Error(w, "500 internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// alert is the alert group's page.
func (s *Server) alert(w http.ResponseWriter, r *http.Request) {
	headers(w)
	a := s.access(r, "status:read")
	if !a.OK || s.Bridge == nil {
		notFound(w)
		return
	}
	id := r.PathValue("id")
	ev, err := s.Bridge.Evidence(r.Context(), id, "")
	if err != nil {
		s.missing(w, r, a.Viewer)
		return
	}
	tl, err := s.Bridge.Timeline(r.Context(), id, "")
	if err != nil {
		s.missing(w, r, a.Viewer)
		return
	}
	g, err := s.Bridge.Group(id)
	if err != nil {
		s.missing(w, r, a.Viewer)
		return
	}
	s.render(w, alertTmpl, s.alertPage(r, ev, tl, g, a))
}

// session is the session's page, with its replay.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	headers(w)
	a := s.access(r, "sessions:read|machines:ssh")
	if !a.OK && a.Viewer != nil && s.SSH != nil {
		s.forbidden(w, r, a.Viewer, "sessions:read")
		return
	}
	if !a.OK || s.SSH == nil {
		notFound(w)
		return
	}
	tr, err := s.SSH.Session(r.PathValue("id"))
	if err != nil {
		s.missing(w, r, a.Viewer)
		return
	}
	s.render(w, sessionTmpl, s.sessionPage(r, tr, a))
}

// recording streams the session's recording (asciicast v3) for the player on its page.
func (s *Server) recording(w http.ResponseWriter, r *http.Request) {
	headers(w)
	a := s.access(r, "sessions:read|machines:ssh")
	if !a.OK || s.SSH == nil {
		notFound(w)
		return
	}
	rc, err := s.SSH.Recording(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.Copy(w, rc)
}

// staticFiles are the files served under /static, by their path there, with their content types.
// Nothing else is.
var staticFiles = map[string]string{
	"asciinema-player.min.js":           "text/javascript; charset=utf-8",
	"asciinema-player.css":              "text/css; charset=utf-8",
	"beacon-player.js":                  "text/javascript; charset=utf-8",
	"beacon.css":                        "text/css; charset=utf-8",
	"status.css":                        "text/css; charset=utf-8",
	"ol/ui.css":                         "text/css; charset=utf-8",
	"ol/fonts/Geist-Variable.woff2":     "font/woff2",
	"ol/fonts/GeistMono-Variable.woff2": "font/woff2",
}

// etags are the static files' entity tags, from their content.
var etags = func() map[string]string {
	out := map[string]string{}
	for name := range staticFiles {
		b, err := static.ReadFile("static/" + name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(b)
		out[name] = `"` + hex.EncodeToString(sum[:8]) + `"`
	}
	return out
}()

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	headers(w)
	want := strings.TrimPrefix(r.URL.Path, "/static/")
	for name, ct := range staticFiles { // the name served comes from the list, never from the request
		if name != want {
			continue
		}
		w.Header().Set("ETag", etags[name])
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if r.Header.Get("If-None-Match") == etags[name] {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		b, _ := static.ReadFile("static/" + name)
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(b)
		return
	}
	notFound(w)
}
