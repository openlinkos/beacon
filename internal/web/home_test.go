package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

// poweredLine is the public pages' line about Beacon, as the footer has it in English.
const poweredLine = `<p class="ol-site-footer-note bc-powered"><a href="https://beacon.openlinkos.com/" data-brand="beacon">`

// signedInCookie is a console session for a token with status:read, as a request's Cookie header.
func (e *statusEnv) signedInCookie() map[string]string {
	e.t.Helper()
	_, tok, err := e.st.CreateToken("alice", []string{"status:read"}, time.Hour, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	id, _, err := e.st.CreateConsoleSession(tok, time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	return map[string]string{"Cookie": sessionCookie + "=" + id}
}

func TestRootIsTheDefaultStatusPage(t *testing.T) {
	e := newStatusEnv(t)
	root, def := e.get("/"), e.get("/status")
	// the same page, whose language links stay where the reader is
	here := strings.NewReplacer(`href="/status"`, `href="/"`, `href="/status?lang=`, `href="/?lang=`).Replace(def.body)
	if root.code != http.StatusOK || root.body != here || !strings.Contains(root.body, `hreflang="zh-CN" href="/?lang=zh"`) {
		t.Fatalf("/ is not what /status serves: %d", root.code)
	}
	for _, h := range []string{"Content-Type", "Cache-Control", "Content-Security-Policy", "Referrer-Policy", "X-Frame-Options", "X-Robots-Tag", "Access-Control-Allow-Origin"} {
		if root.header.Get(h) != def.header.Get(h) {
			t.Errorf("%s: %q at /, %q at /status", h, root.header.Get(h), def.header.Get(h))
		}
	}
	if root.header.Get("Cache-Control") != "public, max-age=30" || root.header.Get("X-Robots-Tag") != "" {
		t.Errorf("/ is not public and cacheable: %v", root.header)
	}
	if again := e.do(http.MethodGet, "beacon.example.com", "/", map[string]string{"If-None-Match": root.header.Get("ETag")}); again.code != http.StatusNotModified {
		t.Errorf("If-None-Match at /: %d", again.code)
	}
	if zh := e.get("/?lang=zh"); !strings.Contains(zh.body, "Example Status ZH") {
		t.Error("/ takes ?lang= as the page does")
	}
}

func TestPublicHeaderSignIn(t *testing.T) {
	e := newStatusEnv(t)
	signIn := `<a class="ol-btn ol-btn-ghost ol-btn-sm" href="/console">Sign in</a>`
	console := `<a class="ol-btn ol-btn-ghost ol-btn-sm" href="/console">Console</a>`
	// signed out: Sign in, on the pages and the incidents', all to /console
	for _, path := range []string{"/", "/status", "/status/public", "/status/public/incidents/" + e.incident} {
		a := e.get(path)
		if !strings.Contains(a.body, signIn) || strings.Contains(a.body, console) {
			t.Errorf("%s: the header has no Sign in", path)
		}
	}
	// signed in: Console, and the page is no longer for shared caches
	cookie := e.signedInCookie()
	for _, path := range []string{"/", "/status/public/incidents/" + e.incident} {
		a := e.do(http.MethodGet, "beacon.example.com", path, cookie)
		if !strings.Contains(a.body, console) || strings.Contains(a.body, signIn) || a.header.Get("Cache-Control") != "private, max-age=30" {
			t.Errorf("%s signed in: %q", path, a.header.Get("Cache-Control"))
		}
	}
	// a cookie that names no session is anyone's
	if a := e.do(http.MethodGet, "beacon.example.com", "/", map[string]string{"Cookie": sessionCookie + "=nope"}); !strings.Contains(a.body, signIn) ||
		a.header.Get("Cache-Control") != "public, max-age=30" {
		t.Error("an unknown session's cookie is taken as signed in")
	}
	// on the page's own host there is no console, and so no way into it
	for _, headers := range []map[string]string{nil, cookie} {
		for _, path := range []string{"/", "/incidents/" + e.incident} {
			if a := e.do(http.MethodGet, "uptime.example.com", path, headers); a.code != http.StatusOK || strings.Contains(a.body, `href="/console`) {
				t.Errorf("uptime.example.com%s links to the console: %d", path, a.code)
			}
		}
	}
	// without the console (no store), no link either
	e.srv.Store = nil
	if a := e.get("/status/public"); strings.Contains(a.body, `href="/console`) {
		t.Error("a link to a console that is not served")
	}
}

func TestPoweredBy(t *testing.T) {
	e := newStatusEnv(t)
	for _, host := range []string{"beacon.example.com", "uptime.example.com"} {
		a := e.do(http.MethodGet, host, "/", nil)
		if !strings.Contains(a.body, poweredLine) || !strings.Contains(a.body, "Powered by Beacon, open source") ||
			!strings.Contains(a.body, `<a href="https://github.com/openlinkos/beacon">GitHub</a>`) || !strings.Contains(a.body, `class="ol-brand-accent"`) {
			t.Errorf("%s: the footer does not say it runs on Beacon", host)
		}
		noInlineScripts(t, a.body)
	}
	zh := e.get("/status/public?lang=zh")
	if !strings.Contains(zh.body, `href="https://beacon.openlinkos.com/zh/" data-brand="beacon"`) || !strings.Contains(zh.body, i18n.T(i18n.ZH, "Powered by Beacon, open source")) {
		t.Error("the Chinese page's line is not in Chinese, or does not lead to the Chinese documentation")
	}
	// a team may leave it out of its public pages
	off := false
	e.srv.Branding = config.Branding{PoweredBy: &off}
	for _, path := range []string{"/", "/status/public/incidents/" + e.incident} {
		if a := e.get(path); a.code != http.StatusOK || strings.Contains(a.body, "Powered by Beacon") || strings.Contains(a.body, "bc-powered") {
			t.Errorf("%s says Powered by Beacon with powered_by: false", path)
		}
	}
}

func TestLandingPage(t *testing.T) {
	e := newEnv(t, i18n.EN)
	a := e.browser().get("/")
	if a.code != http.StatusOK {
		t.Fatalf("/ without status pages: %d %s", a.code, a.body)
	}
	for _, want := range []string{
		`<html lang="en">`, `data-brand="beacon"`, `class="ol-site-header"`, `class="ol-site-footer"`, "<title>Beacon</title>",
		`<h1 class="ol-hero-title" id="hero-title">Beacon</h1>`, "Self-hosted monitoring and SSH access for the AI agents that run your servers.",
		`<a class="ol-btn ol-btn-primary ol-btn-lg" href="/console">`, `<a class="ol-btn ol-btn-ghost ol-btn-sm" href="/console">Sign in</a>`,
		poweredLine, `<a href="https://github.com/openlinkos/beacon">GitHub</a>`,
	} {
		if !strings.Contains(a.body, want) {
			t.Errorf("the landing page lacks %s", want)
		}
	}
	noInlineScripts(t, a.body)
	if strings.Contains(a.body, "feed.atom") || strings.Contains(a.body, `rel="canonical"`) {
		t.Error("the landing page links to a feed or a page that does not exist")
	}
	if a.header.Get("Content-Security-Policy") != statusCSP || a.header.Get("X-Robots-Tag") != "" || a.header.Get("Cache-Control") != "public, max-age=30" {
		t.Errorf("headers: %v", a.header)
	}
	// signed in, the way in says Console; the deployment's own title; and the line can go
	b := e.signedIn("alice", "status:read")
	off := false
	e.srv.Branding = config.Branding{Title: "Example Operations", PoweredBy: &off}
	a = b.get("/")
	if !strings.Contains(a.body, `<h1 class="ol-hero-title" id="hero-title">Example Operations</h1>`) || !strings.Contains(a.body, ">Console</a>") ||
		strings.Contains(a.body, "Powered by Beacon") || a.header.Get("Cache-Control") != "private, max-age=30" {
		t.Errorf("the landing page signed in, titled, without the line: %s", a.body)
	}
	// the console says what it runs on all the same
	if page := b.get("/console"); !strings.Contains(page.body, "Report an issue") {
		t.Error("powered_by: false took the console's footer away")
	}
}

func TestOldPathsMove(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.signedIn("alice", "status:read", "sessions:read")
	for _, old := range []string{
		"/sign-in?next=%2Fconsole%2Ftokens", "/alerts?state=firing&severity=critical", "/alerts/" + e.id + "?done=note", "/machines", "/machines/edge-1?page=2",
		"/sessions?agent=codex", "/sessions/" + sessionID, "/sessions/" + sessionID + "/recording", "/tokens", "/config?before=3",
		"/config/history/1", "/agents?hook=oncall", "/audit?q=add_note", "/changes?source=flux",
	} {
		for _, br := range []*browser{e.browser(), b} {
			a := br.get(old)
			if a.code != http.StatusPermanentRedirect || a.header.Get("Location") != "/console"+old || a.header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s: %d %q", old, a.code, a.header.Get("Location"))
			}
		}
	}
	// followed, the old path is the page it was
	if a := b.get("/console/machines/edge-1?page=2"); a.code != http.StatusOK || !strings.Contains(a.body, "edge-1") {
		t.Errorf("the moved page: %d", a.code)
	}
	if a := b.get("/console/"); a.code != http.StatusPermanentRedirect || a.header.Get("Location") != "/console" {
		t.Errorf("/console/: %d %q", a.code, a.header.Get("Location"))
	}
	// the forms post to the new paths only; nothing is posted to the old ones
	if a := b.post("/alerts/"+e.id+"/notes", nil); a.code != http.StatusNotFound && a.code != http.StatusMethodNotAllowed {
		t.Errorf("a form posted to the old path: %d", a.code)
	}
}

func TestOldSignedLinks(t *testing.T) {
	e := newEnv(t, i18n.EN)
	until := e.now.Add(30 * time.Minute)
	oldLink := func(path string, until time.Time) string {
		return path + "?" + link.Param + "=" + e.signer.SignUntil(http.MethodGet, path, until)
	}
	for _, c := range []struct{ old, now string }{
		{"/alerts/" + e.id, link.AlertPath(e.id)},
		{"/sessions/" + sessionID, link.SessionPath(sessionID)},
		{"/sessions/" + sessionID + "/recording", link.RecordingPath(sessionID)},
	} {
		// a link from the chat, made before the move: on to the new path, signed for it, expiring as it did
		a := e.browser().get(oldLink(c.old, until) + "&lang=en")
		u, err := url.Parse(a.header.Get("Location"))
		if a.code != http.StatusPermanentRedirect || err != nil || u.Path != c.now || u.Query().Get("lang") != "en" {
			t.Fatalf("%s: %d %q", c.old, a.code, a.header.Get("Location"))
		}
		got, ok := e.signer.Verify(http.MethodGet, u.Path, u.Query().Get(link.Param))
		if !ok || !got.Equal(until.Truncate(time.Second)) {
			t.Errorf("%s: the new link does not verify, or expires at %v rather than %v", c.old, got, until)
		}
		if a.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: a signed redirect may be cached", c.old)
		}
		if page := e.browser().get(u.String()); page.code != http.StatusOK {
			t.Errorf("%s: following the new link: %d", c.old, page.code)
		}

		// forged, expired, for another path, or the new path's: no signature goes on, and the page is 404
		for name, bad := range map[string]string{
			"forged":       c.old + "?" + link.Param + "=" + strings.Repeat("A", 10) + ".AAAA",
			"expired":      oldLink(c.old, e.now.Add(-time.Second)),
			"another path": c.old + "?" + e.signer.Query("/alerts/other"),
			"the new path": c.old + "?" + e.signer.Query(c.now),
		} {
			a := e.browser().get(bad)
			u, _ := url.Parse(a.header.Get("Location"))
			if a.code != http.StatusPermanentRedirect || u.Path != c.now || u.Query().Has(link.Param) {
				t.Errorf("%s, %s: %d %q", c.old, name, a.code, a.header.Get("Location"))
				continue
			}
			if page := e.browser().get(u.String()); page.code != http.StatusNotFound {
				t.Errorf("%s, %s: the page opens: %d", c.old, name, page.code)
			}
		}
	}
	// signed in, an expired old link still opens the page, as it did before the move
	b := e.signedIn("alice", "status:read")
	a := b.get(oldLink("/alerts/"+e.id, e.now.Add(-time.Minute)))
	if page := b.get(a.header.Get("Location")); page.code != http.StatusOK {
		t.Errorf("signed in, an expired link: %d", page.code)
	}
	// the links the pages hand out now are under /console
	_, body, _ := e.get(e.link(link.AlertPath(e.id)), "")
	if replays := hrefs(body, "/console/sessions/"+sessionID+"?s="); len(replays) == 0 || len(hrefs(body, "/sessions/")) != 0 {
		t.Errorf("the alert's page links to sessions elsewhere than /console: %v", hrefs(body, "/sessions/"))
	}
	if u := e.b.AlertPage(e.id); !strings.HasPrefix(u, publicURL+"/console/alerts/"+e.id+"?s=") {
		t.Errorf("the card's link: %q", u)
	}
	if u := e.b.SessionPage(sessionID); !strings.HasPrefix(u, publicURL+"/console/sessions/"+sessionID+"?s=") {
		t.Errorf("a session's link: %q", u)
	}
}

func TestConsoleFooter(t *testing.T) {
	for _, c := range []struct {
		lang i18n.Lang
		docs string
	}{{i18n.EN, "https://beacon.openlinkos.com/"}, {i18n.ZH, "https://beacon.openlinkos.com/zh/"}} {
		e := newEnv(t, c.lang)
		footer := `<footer class="ol-footer">
  <span>Beacon v0.3.0-test</span>
  <a href="` + c.docs + `">` + i18n.T(c.lang, "Documentation") + `</a>
  <a href="https://github.com/openlinkos/beacon">GitHub</a>
  <a href="https://github.com/openlinkos/beacon/issues/new/choose">` + i18n.T(c.lang, "Report an issue") + `</a>
</footer>`
		b := e.signedIn("alice", "status:read", "sessions:read")
		for _, path := range []string{"/console", "/console/alerts", "/console/tokens", link.AlertPath(e.id), "/console/machines/nowhere"} {
			if a := b.get(path); !strings.Contains(a.body, footer) {
				t.Errorf("%s %s: no footer with the version and the project's links", c.lang, path)
			}
		}
		// opened with a link, signed out, the page says it too
		if a := e.browser().get(e.link(link.SessionPath(sessionID))); !strings.Contains(a.body, footer) {
			t.Errorf("%s: the page opened with a link has no footer", c.lang)
		}
		// the sign-in page: under the form, Beacon is open source, and where to read and see it
		in := e.browser().get("/console/sign-in")
		for _, want := range []string{
			`<p class="ol-auth-statement">` + i18n.T(c.lang, "Sign in to see your services, alerts and servers, and everything your agents did on them.") + `</p>`,
			`<div class="ol-auth-foot"><span>Beacon v0.3.0-test</span><span>` + i18n.T(c.lang, "Beacon is open source.") + `</span>`,
			`<a href="` + c.docs + `">` + i18n.T(c.lang, "Documentation") + `</a><a href="https://github.com/openlinkos/beacon">GitHub</a></div>`,
		} {
			if !strings.Contains(in.body, want) {
				t.Errorf("%s: the sign-in page lacks %s", c.lang, want)
			}
		}
	}
	if versionText("0.3.0-rc.12") != "v0.3.0-rc.12" || versionText("dev") != "dev" {
		t.Error("versionText")
	}
}
