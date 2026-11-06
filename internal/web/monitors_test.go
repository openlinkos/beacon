package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/web/link"
)

// The console's monitors and status pages: what is probed and how it is doing, and the pages the public
// sees, read-only, from the same configuration and probes as the status pages.
func TestConsoleMonitors(t *testing.T) {
	e := newStatusEnv(t)
	cookie := e.signedInCookie()
	get := func(path string) statusAnswer {
		t.Helper()
		a := e.do(http.MethodGet, "beacon.example.com", path, cookie)
		if a.code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, a.code, a.body)
		}
		checkPage(t, path, a.body)
		if strings.Contains(a.body, "<img src=x") || strings.Contains(a.body, "<script>alert") {
			t.Errorf("%s shows a page's text unescaped", path)
		}
		return a
	}

	// every service the target sets name, the one in trouble first, with what the locations see
	list := get("/console/monitors")
	for _, want := range []string{
		`href="/console/monitors/api.example.com"`, `href="/console/monitors/docker.example.com"`,
		"Failing at 2 of 2 locations", `ol-badge-danger">Failing at 2 of 2 locations`, "https://api.example.com/healthz",
		`href="` + link.AlertPath(e.incident) + `"`, // the service's firing alert
		`href="/console/status-pages/public"`,       // the pages that show it
		"2 services, 1 in trouble",
	} {
		if !strings.Contains(list.body, want) {
			t.Errorf("/console/monitors lacks %s", want)
		}
	}
	if i, j := strings.Index(list.body, "/console/monitors/api.example.com"), strings.Index(list.body, "/console/monitors/docker.example.com"); i > j {
		t.Error("the service in trouble is not listed first")
	}
	if !strings.Contains(list.body, `ol-badge-success">OK`) {
		t.Error("docker.example.com is not OK")
	}

	// one service: each location's result, its targets, its pages and its alerts
	one := get("/console/monitors/api.example.com")
	for _, want := range []string{"edge-1", "edge-2", "Failing", "https://api.example.com/healthz", "all-web"} {
		if !strings.Contains(one.body, want) {
			t.Errorf("/console/monitors/api.example.com lacks %s", want)
		}
	}
	if !strings.Contains(one.body, `href="`+link.AlertPath(e.incident)+`"`) {
		t.Error("the service's page does not link its alert")
	}
	if a := e.do(http.MethodGet, "beacon.example.com", "/console/monitors/nowhere.example.com", cookie); a.code != http.StatusNotFound {
		t.Errorf("a service nothing probes: %d", a.code)
	}

	// the status pages: where each is, how it is doing, what it holds
	pages := get("/console/status-pages")
	for _, want := range []string{"Example Status", "https://uptime.example.com/", `href="/console/status-pages/public"`} {
		if !strings.Contains(pages.body, want) {
			t.Errorf("/console/status-pages lacks %s", want)
		}
	}
	page := get("/console/status-pages/public")
	for _, want := range []string{"Mirrors", `href="/console/monitors/docker.example.com"`, `href="/console/monitors/api.example.com"`,
		`href="` + link.AlertPath(e.incident) + `"`, "https://uptime.example.com/", "Availability (90 days)"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("/console/status-pages/public lacks %s", want)
		}
	}
	if a := e.do(http.MethodGet, "beacon.example.com", "/console/status-pages/nowhere", cookie); a.code != http.StatusNotFound {
		t.Errorf("a page that does not exist: %d", a.code)
	}

	// signed out, they send people to sign in
	if a := e.get("/console/monitors"); a.code != http.StatusSeeOther {
		t.Errorf("signed out: %d", a.code)
	}
}
