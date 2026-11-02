// Package web serves the pages people open from the chat: an alert's page, with its timeline and the
// sessions on its machines, and a session's page, with its replay. They are HTML from embedded
// templates, without a build step or a framework, served by linkos-beacon beside the API on the same
// port, in the configured language, readable on a phone.
//
// There is no login. A page opens with a signed link (package link), which the card, the chat echoes
// and the API hand out, or with a bearer token carrying the scope the matching API route needs; anything
// else is 404, so that a wrong or expired link reveals nothing, not even that the page exists. Text from
// outside Beacon reaches the pages cleaned by the bridge and is escaped by html/template on the way out;
// the pages carry no inline script, and their Content-Security-Policy allows nothing from elsewhere.
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
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/web/link"
)

//go:embed templates/*.html
var templates embed.FS

//go:embed static
var static embed.FS

var (
	alertTmpl   = template.Must(template.ParseFS(templates, "templates/layout.html", "templates/alert.html"))
	sessionTmpl = template.Must(template.ParseFS(templates, "templates/layout.html", "templates/session.html"))
)

// CSP is the pages' Content-Security-Policy: scripts and styles from Beacon itself only (the player's
// terminal runs as WebAssembly, hence wasm-unsafe-eval), no inline script, fetches to Beacon only.
const CSP = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// tokenTTL is how long the links inside a page last (to sessions, to the recording the player fetches)
// when the page was opened with a token rather than a signed link; opened with a link, they last as long
// as it does.
const tokenTTL = time.Hour

// Server serves the pages. SSH is nil when the channel is off (session pages are then 404); Auth checks
// bearer tokens (nil: only signed links open pages); Pages checks the links' signatures and signs the
// links inside pages (nil: only tokens open pages, and pages link nowhere).
type Server struct {
	Bridge   *bridge.Bridge
	SSH      *sshgate.Gate
	Auth     *api.Auth
	Pages    *link.Pages
	Lang     i18n.Lang
	Location *time.Location
	Log      *slog.Logger
	Now      func() time.Time
}

// Register adds the pages' routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /alerts/{id}", s.alert)
	mux.HandleFunc("GET /sessions/{id}", s.session)
	mux.HandleFunc("GET /sessions/{id}/recording", s.recording)
	mux.HandleFunc("GET /static/{file}", s.static)
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

// access is how a request may see a page: through a signed link or a token (Token names it). Until is
// how long the links inside the page last: as long as the link that opened it, or tokenTTL.
type access struct {
	OK    bool
	Until time.Time
	Token string
}

// access checks the request against the signed link it carries, then against a bearer token with one of
// the scopes ("a|b").
func (s *Server) access(r *http.Request, scopes string) access {
	if sig := r.URL.Query().Get(link.Param); sig != "" && s.Pages != nil && s.Pages.Signer != nil {
		if until, ok := s.Pages.Signer.Verify(http.MethodGet, r.URL.Path, sig); ok {
			return access{OK: true, Until: until}
		}
	}
	if secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.Auth != nil {
		if t, ok := s.Auth.Verify(r, strings.TrimSpace(secret)); ok && slices.ContainsFunc(strings.Split(scopes, "|"), t.Has) {
			return access{OK: true, Token: t.Name, Until: s.now().Add(tokenTTL)}
		}
	}
	return access{}
}

// signed returns path with a signature that lasts until the given time; without a signer, the path alone
// (then only a token opens it).
func (s *Server) signed(path string, until time.Time) string {
	if s.Pages == nil || s.Pages.Signer == nil {
		return path
	}
	return path + "?" + link.Param + "=" + s.Pages.Signer.SignUntil(http.MethodGet, path, until)
}

func (s *Server) render(w http.ResponseWriter, t *template.Template, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.log().Error("page template failed", "err", err)
		http.Error(w, "500 internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
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
		notFound(w)
		return
	}
	tl, err := s.Bridge.Timeline(r.Context(), id, "")
	if err != nil {
		notFound(w)
		return
	}
	g, err := s.Bridge.Group(id)
	if err != nil {
		notFound(w)
		return
	}
	s.render(w, alertTmpl, s.alertPage(ev, tl, g, a))
}

// session is the session's page, with its replay.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	headers(w)
	a := s.access(r, "sessions:read|machines:ssh")
	if !a.OK || s.SSH == nil {
		notFound(w)
		return
	}
	tr, err := s.SSH.Session(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return
	}
	s.render(w, sessionTmpl, s.sessionPage(tr, a))
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

// staticFiles are the files served under /static, with their content types. Nothing else is.
var staticFiles = map[string]string{
	"asciinema-player.min.js": "text/javascript; charset=utf-8",
	"asciinema-player.css":    "text/css; charset=utf-8",
	"beacon-player.js":        "text/javascript; charset=utf-8",
	"beacon.css":              "text/css; charset=utf-8",
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
	want := r.PathValue("file")
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
