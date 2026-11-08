package web

import (
	"net/http"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The site's root, and the console's old paths. / is public: the default status page, as /status
// serves it, unless that page has a host name of its own (it lives there, and Beacon's own host is
// then the console's); otherwise a page that names the deployment, says what Beacon is, lists the
// status pages it serves and offers the console's sign-in. The console was at the root until 0.3.0; its old paths send people on to
// /console, and the links handed out until then (signed for /alerts/{id} and /sessions/{id}) keep
// working until they expire.

var landingTmpl = parseStatus("landing.html")

// home is the site's root.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if s.Status != nil {
		if def, ok := s.Status.Def(""); ok && def.Host == "" {
			s.statusPage(w, r, "", false)
			return
		}
	}
	s.landing(w, r)
}

// landing is the page at / without a status page there, on the family's site design as the status pages are.
func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	l := s.Lang
	name := s.Branding.Name()
	v := &statusView{Lang: l.HTML(), Title: name, Name: name, Brand: "beacon", Version: s.version(), Icons: statusSprite,
		L: statusLabelsIn(l), Root: "/", Description: s.t("Self-hosted infrastructure for AI agents, shared with the people who manage them.")}
	v.Mark, v.MarkHero = marks(v.Brand)
	v.SignIn, v.private = s.consoleLink(r, l, false)
	v.Powered = s.poweredBy(l)
	for _, p := range s.Status.Pages() {
		v.Pages = append(v.Pages, linkView{Title: p.Title.In(string(l)), URL: s.Status.URL(p, "")})
	}
	s.renderPublic(w, r, landingTmpl, v)
}

// consoleLink is the public header's way into the console: Console for someone signed in, Sign in for
// anyone else, both to /console, which sends whoever is not signed in to sign in. There is none on a
// status page's own host, where the console does not answer, nor without the console. Private says the
// link names the reader's sign-in.
func (s *Server) consoleLink(r *http.Request, l i18n.Lang, onHost bool) (_ *linkView, private bool) {
	if onHost || !s.Console() {
		return nil, false
	}
	if s.viewer(r) != nil {
		return &linkView{Title: i18n.T(l, "Console"), URL: consolePath("")}, true
	}
	return &linkView{Title: i18n.T(l, "Sign in"), URL: consolePath("")}, false
}

// movedPaths are the console's pages as they were before it moved under /console.
var movedPaths = []string{"/sign-in", "/alerts", "/alerts/{id}", "/machines", "/machines/{name}", "/sessions", "/sessions/{id}",
	"/sessions/{id}/recording", "/tokens", "/config", "/config/history/{version}", "/agents", "/audit", "/changes"}

func (s *Server) registerMoved(mux *http.ServeMux) {
	for _, p := range movedPaths {
		mux.HandleFunc("GET "+p, s.moved)
	}
}

// moved sends a request for an old path on to the page's place under /console, with its query. A
// signed link to an alert's or a session's page names the old path inside its signature: one that
// verifies there goes on with a signature for the new path that expires when the old one does. Any
// other signature (forged, expired, for another path) is dropped, and the page then judges the request
// as it does any other: a plain 404, unless the reader is signed in or sends a token.
func (s *Server) moved(w http.ResponseWriter, r *http.Request) {
	headers(w)
	w.Header().Set("Cache-Control", "no-store")
	to, query := consolePath(r.URL.EscapedPath()), r.URL.RawQuery
	if r.URL.Path == "/agents" { // the hooks' page, which was called Agents before agents had pages of their own
		to = consolePath("/hooks")
	}
	if q := r.URL.Query(); q.Has(link.Param) {
		sig := q.Get(link.Param)
		q.Del(link.Param)
		if now, ok := link.Moved(r.URL.Path); ok && s.Pages != nil && s.Pages.Signer != nil {
			if until, ok := s.Pages.Signer.Verify(http.MethodGet, r.URL.Path, sig); ok {
				q.Set(link.Param, s.Pages.Signer.SignUntil(http.MethodGet, now, until))
			}
		}
		query = q.Encode()
	}
	if query != "" {
		to += "?" + query
	}
	http.Redirect(w, r, to, http.StatusPermanentRedirect) //nolint:gosec // G710: always a path under /console on this site
}
