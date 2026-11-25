package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/status"
)

func TestConsoleStatusComponentPublication(t *testing.T) {
	e := newStatusEnvWith(t, strings.Replace(statusCatalogue, "    default: true", "    default: true\n    notes: all", 1))
	cfg := e.srv.Config
	actor := configdb.Actor{Name: "fixture", Via: "cli"}
	if _, err := cfg.Put(context.Background(), actor, configdb.KindTargets, "all-billing", json.RawMessage(`[{
		"targets":["https://internal.example.com/health"],"labels":{"module":"http_head","service":"billing","visibility":"internal"}
	}]`), ""); err != nil {
		t.Fatal(err)
	}
	makeSession := func(name string, scopes ...string) (cookie, csrf string) {
		t.Helper()
		_, token, err := e.st.CreateToken(name, scopes, time.Hour, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		id, session, err := e.st.CreateConsoleSession(token, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return sessionCookie + "=" + id, session.CSRF
	}
	call := func(method, path, cookie string, form url.Values) statusAnswer {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Host = "beacon.example.com"
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		return statusAnswer{code: rec.Code, body: rec.Body.String(), header: rec.Header()}
	}
	const (
		page      = "/console/status-pages/public"
		add       = page + "/components/add"
		edit      = page + "/components/billing/edit"
		remove    = page + "/components/billing/remove"
		public    = "/status/public"
		component = `id="component-billing"`
	)
	viewer, viewerCSRF := makeSession("page-reader", "status:read")
	owner, ownerCSRF := makeSession("page-owner", "status:read", "config:write")
	version := cfg.Version()
	data := url.Values{"intent": {"publish"}, "version": {strconv.FormatInt(version, 10)},
		"component": {"billing"}, "title": {"Billing & reports"}, "service": {"billing"}, "csrf": {viewerCSRF}}

	if a := call("GET", page, viewer, nil); a.code != http.StatusOK || strings.Contains(a.body, add) {
		t.Fatal("the reader did not get a read-only status page detail")
	}
	if a := call("GET", add, owner, nil); a.code != http.StatusOK || !strings.Contains(a.body, "Add component") {
		t.Fatalf("the owner could not open the component editor: %d", a.code)
	}
	if a := call("POST", add, viewer, data); a.code != http.StatusForbidden || cfg.Version() != version {
		t.Fatal("a reader changed the status page")
	}
	if a := call("POST", add, owner, data); a.code != http.StatusForbidden || cfg.Version() != version {
		t.Fatal("an owner's form with a different session's CSRF token changed the status page")
	}
	data.Set("csrf", ownerCSRF)
	data.Set("component", "api")
	data.Set("title", " ")
	if a := call("POST", add, owner, data); a.code != http.StatusBadRequest || cfg.Version() != version {
		t.Fatal("an add request with no title edited an existing component")
	}
	data.Set("component", "billing")
	data.Set("title", "Billing & reports")
	data.Set("intent", "preview")
	a := call("POST", add, owner, data)
	for _, want := range []string{"Billing &amp; reports", "Newly visible services", "billing", "publishes all incident notes"} {
		if a.code != http.StatusOK || !strings.Contains(a.body, want) {
			t.Errorf("preview status %d, wanted %q", a.code, want)
		}
	}
	if a := call("GET", public, "", nil); a.code != http.StatusOK || strings.Contains(a.body, component) || cfg.Version() != version {
		t.Fatal("preview changed the public page or configuration version")
	}
	data.Set("intent", "publish")
	data.Set("version", strconv.FormatInt(version-1, 10))
	if a := call("POST", add, owner, data); a.code != http.StatusConflict || cfg.Version() != version {
		t.Fatal("a stale submission changed the public page")
	}
	data.Set("version", strconv.FormatInt(version, 10))
	a = call("POST", add, owner, data)
	if a.code != http.StatusSeeOther || a.header.Get("Location") != page+"?done=status_page_updated" || cfg.Version() != version+1 ||
		!audited(e.st, "page-owner", "config_put") {
		t.Fatalf("publishing the new component: status %d, version %d", a.code, cfg.Version())
	}
	if a := call("GET", public, "", nil); a.code != http.StatusOK || !strings.Contains(a.body, component) ||
		!strings.Contains(a.body, "Billing &amp; reports") || strings.Contains(a.body, "https://internal.example.com/health") {
		t.Fatal("the signed-out page did not show precisely the public component details")
	}
	a = call("GET", public+".json", "", nil)
	var jsonPage status.Page
	if err := json.Unmarshal([]byte(a.body), &jsonPage); a.code != http.StatusOK || err != nil {
		t.Fatalf("reading signed-out JSON: status %d, error %v", a.code, err)
	}
	if published, ok := jsonPage.Component("billing"); !ok || published.Title != "Billing & reports" ||
		strings.Contains(a.body, "https://internal.example.com/health") {
		t.Fatal("the signed-out JSON did not show precisely the public component details")
	}
	data.Set("version", strconv.FormatInt(cfg.Version(), 10))
	data.Set("component", "mirrors")
	data.Set("title", "Forged title")
	data.Add("service", "docker.example.com")
	if a := call("POST", edit, owner, data); a.code != http.StatusSeeOther {
		t.Fatalf("editing explicit services: status %d", a.code)
	}
	def, _ := cfg.State().Catalog.StatusPage("public")
	billing, ok := def.Component("billing")
	if !ok || billing.Title.In("en") != "Billing & reports" || len(billing.Services) != 2 {
		t.Fatal("the services or the preserved component title do not match")
	}
	data.Set("version", strconv.FormatInt(cfg.Version(), 10))
	data.Set("intent", "preview")
	a = call("POST", remove, owner, data)
	if a.code != http.StatusOK || !strings.Contains(a.body, "Services that will no longer be visible") ||
		!strings.Contains(a.body, "Services visible after publishing") {
		t.Fatalf("removing a component must preview the remaining visible services: status %d", a.code)
	}
	data.Set("intent", "publish")
	if a := call("POST", remove, owner, data); a.code != http.StatusSeeOther {
		t.Fatalf("removing the new component: status %d", a.code)
	}
	if a := call("GET", public, "", nil); a.code != http.StatusOK || strings.Contains(a.body, component) ||
		!strings.Contains(a.body, `id="component-mirrors"`) || !strings.Contains(a.body, `id="component-api"`) {
		t.Fatal("removing a component removed or changed another public component")
	}
}
