package web

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/version"
)

// The shell every page shares: the design system's workbench (.ol-app-workbench), with the sections in
// a narrow sidebar with counts, and the top bar as the page's header (the breadcrumb, the one h1, a
// count, the actions). It adapts to the width by CSS alone (a full sidebar from 1200px, a rail of icons
// from 860px, a sheet below), so the server renders one page for every screen.

type crumb struct{ Label, Href string }

type navItem struct {
	Key, Label, Href, Icon, Count string
	Current                       bool
}

type navGroup struct {
	Label string
	Items []navItem
}

type badge struct{ Text, Tone string }

// sideStatus is the line at the foot of the sidebar: how the SSH channel stands.
type sideStatus struct{ Text, Tone, Href string }

// shellLabels are the shell's own texts.
type shellLabels struct {
	Skip, Menu, Sections, Breadcrumb, SignOut, Close, Filters, Clear, Pages, Previous, Next string
	Account, Scopes                                                                         string
}

type shell struct {
	Lang, Title, Heading, Count string
	Badges                      []badge
	Crumbs                      []crumb
	Nav                         []navGroup
	SignedIn                    bool
	Who, WhoMeta, WhoScopes     string
	CSRF, Version               string
	Access                      string // how a page opened without signing in was opened
	Side                        sideStatus
	Toast, ToastTone            string
	Problem                     string // what went wrong with the form just sent
	S                           shellLabels
	Icons                       template.HTML
	Bare                        bool // the sign-in page: the form beside the brand panel
	Statement, Host             string
}

func (s *Server) shellLabels() shellLabels {
	return shellLabels{
		Skip: s.t("Skip to content"), Menu: s.t("Menu"), Sections: s.t("Sections"), Breadcrumb: s.t("Breadcrumb"),
		SignOut: s.t("Sign out"), Close: s.t("Close"), Filters: s.t("Filters"),
		Clear: s.t("Clear"), Pages: s.t("Pages"), Previous: s.t("Previous page"), Next: s.t("Next page"), Account: s.t("Your token"),
		Scopes: s.t("Scopes"),
	}
}

func (s *Server) version() string {
	if s.Version != "" {
		return s.Version
	}
	return version.Version
}

func (s *Server) baseShell(title string) shell {
	return shell{Lang: s.Lang.HTML(), Title: title + " – Beacon", Heading: title, S: s.shellLabels(), Icons: sprite, Version: s.version()}
}

// shellFor is the shell of a console page for v, in the section nav, under the pages crumbs names.
func (s *Server) shellFor(r *http.Request, v *viewer, nav, title string, crumbs ...crumb) shell {
	sh := s.baseShell(title)
	sh.Crumbs = append([]crumb{{Label: s.t("Console"), Href: "/"}}, crumbs...)
	if v == nil {
		return sh
	}
	sh.SignedIn, sh.CSRF = true, v.Session.CSRF
	sh.Who = v.Token.Name
	sh.WhoMeta = s.t("Signed in until %s", s.short(v.Session.Expires))
	sh.WhoScopes = strings.Join(v.Token.Scopes, " ")
	sh.Side = s.channelStatus()
	sh.Nav = s.nav(nav)
	sh.Toast, sh.ToastTone = s.toast(r.URL.Query().Get("done"))
	sh.Problem = s.problem(r.URL.Query().Get("error"))
	return sh
}

// viewOnly is the shell of an alert's or a session's page opened without signing in, with a signed link
// or a token: the page alone, and a way to sign in for the rest.
func (s *Server) viewOnly(kind, title string, a access) shell {
	sh := s.baseShell(title)
	sh.Crumbs = []crumb{{Label: kind}}
	if a.Token != "" {
		sh.Access = s.t("Opened with API access")
	} else {
		sh.Access = s.t("This link is valid until %s", s.clock(a.Until))
	}
	if s.Console() {
		sh.Nav = []navGroup{{Items: []navItem{{Key: "sign-in", Label: s.t("Sign in"), Href: "/sign-in", Icon: "log-in"}}}}
	}
	return sh
}

// bare is the shell of the sign-in page.
func (s *Server) bare(r *http.Request, title string) shell {
	sh := s.baseShell(title)
	sh.Bare = true
	sh.Statement = s.t("Where agents run operations, and people watch.")
	if s.Pages != nil && s.Pages.PublicURL != "" {
		sh.Host = strings.TrimPrefix(strings.TrimPrefix(s.Pages.PublicURL, "https://"), "http://")
	} else {
		sh.Host = r.Host
	}
	return sh
}

func (s *Server) nav(current string) []navGroup {
	firing := 0
	for _, n := range s.Bridge.Summarize().Firing {
		firing += n
	}
	machines, tokens := len(s.Store.Machines()), len(s.Store.Tokens())
	groups := []navGroup{
		{Items: []navItem{
			{Key: "overview", Label: s.t("Overview"), Href: "/", Icon: "layout-dashboard"},
			{Key: "alerts", Label: s.t("Alerts"), Href: "/alerts", Icon: "triangle-alert", Count: count(firing)},
			{Key: "machines", Label: s.t("Machines"), Href: "/machines", Icon: "server", Count: count(machines)},
			{Key: "sessions", Label: s.t("Sessions"), Href: "/sessions", Icon: "terminal"},
		}},
		{Label: s.t("Records"), Items: []navItem{
			{Key: "agents", Label: s.t("Agents"), Href: "/agents", Icon: "bot"},
			{Key: "changes", Label: s.t("Changes"), Href: "/changes", Icon: "history"},
			{Key: "audit", Label: s.t("Audit log"), Href: "/audit", Icon: "scroll-text"},
		}},
		{Label: s.t("Settings"), Items: []navItem{
			{Key: "config", Label: s.t("Configuration"), Href: "/config", Icon: "file-cog"},
			{Key: "tokens", Label: s.t("Tokens"), Href: "/tokens", Icon: "key-round", Count: count(tokens)},
		}},
	}
	for i := range groups {
		for j := range groups[i].Items {
			groups[i].Items[j].Current = groups[i].Items[j].Key == current
		}
	}
	return groups
}

// count is a figure for the sidebar; none for zero.
func count(n int) string {
	if n == 0 {
		return ""
	}
	return num(n)
}

// channelStatus says how the SSH channel stands.
func (s *Server) channelStatus() sideStatus {
	switch {
	case s.SSH == nil:
		return sideStatus{Text: s.t("The SSH channel is off"), Href: "/machines"}
	case s.SSH.Paused():
		return sideStatus{Text: s.t("Agents paused since %s", s.short(s.SSH.PausedSince())), Tone: "danger", Href: "/machines"}
	}
	return sideStatus{Text: s.t("Agents reach machines"), Tone: "success", Href: "/machines"}
}

// toast is the confirmation shown after a form did what it said.
func (s *Server) toast(done string) (string, string) {
	switch done {
	case "ack":
		return s.t("Alert acknowledged"), "success"
	case "silence":
		return s.t("Alert silenced"), "success"
	case "note":
		return s.t("Note added"), "success"
	case "paused":
		return s.t("Agents' access to machines is paused"), "warning"
	case "resumed":
		return s.t("Agents' access to machines is resumed"), "success"
	case "revoked":
		return s.t("Token revoked"), "success"
	case "reverted":
		return s.t("Change reverted"), "success"
	}
	return "", ""
}

// num writes a whole number with thousands separators.
func num(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ---- pages that only say something

type messagePage struct {
	shell
	Text, Back, BackHref string
}

var messageTmpl = parse("message.html")

// message is a page in the shell that says what went wrong and offers the way back.
func (s *Server) message(w http.ResponseWriter, r *http.Request, v *viewer, status int, title, text string) {
	p := messagePage{shell: s.shellFor(r, v, "", title), Text: text, Back: s.t("Back to the overview"), BackHref: "/"}
	s.renderStatus(w, status, messageTmpl, p)
}

// forbidden says which scope a page or a form needs that the viewer's token lacks.
func (s *Server) forbidden(w http.ResponseWriter, r *http.Request, v *viewer, scope string) {
	s.message(w, r, v, http.StatusForbidden, s.t("Not with this token"),
		s.t("This needs a token with %s; token %s has %s.", strings.ReplaceAll(scope, "|", " | "), v.Token.Name, strings.Join(v.Token.Scopes, ", ")))
}

// missing is a page that does not exist: in the shell for someone signed in, the plain 404 for anyone
// else, which says nothing.
func (s *Server) missing(w http.ResponseWriter, r *http.Request, v *viewer) {
	if v == nil {
		notFound(w)
		return
	}
	s.message(w, r, v, http.StatusNotFound, s.t("Not found"), s.t("There is nothing here, or no longer."))
}

// ---- time

func (s *Server) day(t time.Time) string { return t.In(s.loc()).Format("2006-01-02") }

// today is the start of the day now, where the configuration says Beacon is.
func (s *Server) today() time.Time {
	n := s.now().In(s.loc())
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, s.loc())
}

// ago says how long ago, roughly.
func (s *Server) ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return s.t("%s ago", s.dur(s.now().Sub(t)))
}
