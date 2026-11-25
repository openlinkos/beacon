package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
)

const newStatusPagePath = "/console/status-pages/new/"

func statusPageCreator(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, i18n.EN)
	e.srv.Status = status.New(status.Deps{Config: e.cdb, Bridge: e.b, Store: e.st, Now: func() time.Time { return e.now }, PublicURL: publicURL})
	e.mux = http.NewServeMux()
	e.srv.Register(e.mux)
	return e
}

func statusPageConfirmation(t *testing.T, a answer, code int) string {
	t.Helper()
	form := regexp.MustCompile(`name="confirmation" value="([0-9a-f]{64})"`).FindStringSubmatch(a.body)
	if a.code != code || len(form) != 2 {
		t.Fatalf("expected publication preview with status %d, got %d", code, a.code)
	}
	return form[1]
}

func TestCreateFirstPublicStatusPage(t *testing.T) {
	e := statusPageCreator(t)
	owner := e.owner()
	reader := e.invite(owner, "viewer", "viewer")
	const index = "/console/status-pages"
	if a := owner.get(index); a.code != http.StatusOK || !strings.Contains(a.body, `href="`+newStatusPagePath+`"`) {
		t.Fatalf("the empty list did not offer creation to the owner: %d", a.code)
	}
	if a := reader.get(index); a.code != http.StatusOK || strings.Contains(a.body, newStatusPagePath) {
		t.Fatalf("the reader received a creation action: %d", a.code)
	}
	if a := reader.get(newStatusPagePath); a.code != http.StatusForbidden {
		t.Fatalf("reader reached the creation form: %d", a.code)
	}
	if a := owner.get(newStatusPagePath); a.code != http.StatusOK || !strings.Contains(a.body, "Add an HTTP monitor before creating a status page.") ||
		!strings.Contains(a.body, `href="/console/monitors"`) {
		t.Fatalf("the creation form did not explain the missing service requirement: %d", a.code)
	}

	ctx := context.Background()
	if _, err := e.cdb.Put(ctx, configdb.Actor{Name: "fixture", Via: "cli"}, configdb.KindModule, "http_2xx",
		json.RawMessage(`{"prober":"http"}`), ""); err != nil {
		t.Fatal(err)
	}
	if a := owner.post("/console/monitors", url.Values{"service": {"first-app"}, "url": {"https://example.com/private-health"},
		"scope": {"all"}, "module": {"http_2xx"}, "severity": {"warning"}, "version": {strconv.FormatInt(e.cdb.Version(), 10)}}); a.code != http.StatusSeeOther {
		t.Fatalf("adding the private monitor: %d", a.code)
	}
	if a := owner.get(newStatusPagePath); a.code != http.StatusOK || !strings.Contains(a.body, `value="first-app"`) {
		t.Fatalf("the new monitor was not available as an explicit service: %d", a.code)
	}
	plan := func(title string) {
		t.Helper()
		_, err := e.b.ScheduleMaintenance(ctx, bridge.Actor{Name: "alice", Via: "console"}, bridge.MaintenanceInput{
			Title: title, Description: "The service might be briefly unavailable.", Services: []string{"first-app"},
			StartsAt: e.now.Add(time.Hour), EndsAt: e.now.Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	plan("Scheduled API maintenance")

	incident := store.Incident{ID: "a1", AlertName: "ServiceDown", Status: "resolved", Services: []string{"first-app"},
		StartsAt: e.now.Add(-time.Hour), ResolvedAt: e.now, Notes: []store.IncidentNote{
			{At: e.now, Text: "public: Service restored before publication"},
			{At: e.now, Text: "Internal-only incident note"},
		}}
	if _, err := e.st.PutIncidents([]store.Incident{incident}, e.now.Add(-store.KeepStatus)); err != nil {
		t.Fatal(err)
	}

	absent := func() {
		t.Helper()
		for _, path := range []string{"/status", "/status.json", "/status/first-app", "/status/first-app.json", "/status/first-app/incidents/a1"} {
			if code, _, _ := e.get(path, ""); code != http.StatusNotFound {
				t.Fatalf("unsigned %s existed before the owner published: %d", path, code)
			}
		}
		if code, body, _ := e.get("/", ""); code != http.StatusOK || !strings.Contains(body, "Self-hosted monitoring and SSH access") ||
			strings.Contains(body, "First public page") {
			t.Fatalf("empty default still needs its landing page: %d", code)
		}
	}
	absent()
	version := e.cdb.Version()
	data := url.Values{"name": {"first-app"}, "title": {"First public page"}, "service": {"first-app"},
		"intent": {"preview"}, "version": {strconv.FormatInt(version, 10)}}
	if a := owner.do(http.MethodPost, newStatusPagePath, data); a.code != http.StatusForbidden {
		t.Fatalf("submission without CSRF: %d", a.code)
	}
	if a := reader.post(newStatusPagePath, data); a.code != http.StatusForbidden {
		t.Fatalf("reader could preview a new publication: %d", a.code)
	}
	data.Del("csrf") // the browser fixture attaches the posting session's CSRF to the values
	data.Set("service", "missing")
	if a := owner.post(newStatusPagePath, data); a.code != http.StatusBadRequest {
		t.Fatalf("a made-up service entered the public draft: %d", a.code)
	}
	data.Set("service", "first-app")
	data.Set("name", "bad.path")
	if a := owner.post(newStatusPagePath, data); a.code != http.StatusBadRequest {
		t.Fatalf("an invalid URL path entered the public draft: %d", a.code)
	}
	data.Set("name", "first-app")
	a := owner.post(newStatusPagePath, data)
	for _, want := range []string{"Effect on the public page", publicURL + "/status/first-app", "will also open at / and /status",
		"Maintenance announcements that will become public", "Scheduled API maintenance", "Only incident notes marked public",
		"Incident updates that will become public", "Service restored before publication"} {
		if !strings.Contains(a.body, want) {
			t.Errorf("the preview did not show %q", want)
		}
	}
	if strings.Contains(a.body, "Internal-only incident note") {
		t.Fatal("the public preview included a private incident note")
	}
	confirmation := statusPageConfirmation(t, a, http.StatusOK)
	absent()

	data.Set("intent", "publish")
	statusPageConfirmation(t, owner.post(newStatusPagePath, data), http.StatusConflict)
	data.Set("confirmation", confirmation)
	data.Set("title", "Different public page")
	statusPageConfirmation(t, owner.post(newStatusPagePath, data), http.StatusConflict)
	data.Set("title", "First public page")
	plan("Later announcement")
	a = owner.post(newStatusPagePath, data)
	confirmation = statusPageConfirmation(t, a, http.StatusConflict)
	if !strings.Contains(a.body, "Later announcement") || e.cdb.Version() != version {
		t.Fatal("the fresh announcement was missing from the conflict review or the page was already published")
	}
	absent()
	data.Set("confirmation", confirmation)
	data.Set("version", strconv.FormatInt(version-1, 10))
	if a := owner.post(newStatusPagePath, data); a.code != http.StatusConflict {
		t.Fatalf("a stale configuration version could publish: %d", a.code)
	}
	data.Set("version", strconv.FormatInt(version, 10))
	data.Set("notes", "all")
	data.Set("default", "false")
	data.Set("host", "unrelated.example.com")
	a = owner.post(newStatusPagePath, data)
	if a.code != http.StatusSeeOther || a.header.Get("Location") != index+"/first-app?done=status_page_created" ||
		e.cdb.Version() != version+1 || !audited(e.st, "alice", "config_put") {
		t.Fatalf("actual publication failed: %d, %s, version %d", a.code, a.header.Get("Location"), e.cdb.Version())
	}
	if a := owner.get(a.header.Get("Location")); a.code != http.StatusOK || !strings.Contains(a.body, "Status page created") {
		t.Fatalf("publication did not continue to management: %d", a.code)
	}
	definition, ok := e.cdb.Catalog().StatusPage("first-app")
	if !ok || !definition.Default || definition.Host != "" || definition.Notes != "public" ||
		len(definition.Components) != 1 || definition.Components[0].Name != "services" {
		t.Fatalf("unexpected first page policy, alias or component: %+v", definition)
	}
	for _, path := range []string{"/", "/status", "/status/first-app"} {
		code, body, _ := e.get(path, "")
		if code != http.StatusOK || !strings.Contains(body, "First public page") || !strings.Contains(body, "Monitored services") ||
			!strings.Contains(body, "Later announcement") || strings.Contains(body, "https://example.com/private-health") {
			t.Fatalf("unsigned %s did not show the reviewed page: %d", path, code)
		}
	}
	for _, path := range []string{"/status.json", "/status/first-app.json"} {
		code, body, _ := e.get(path, "")
		var page status.Page
		if err := json.Unmarshal([]byte(body), &page); code != http.StatusOK || err != nil ||
			page.Name != "first-app" || len(page.Components) != 1 || len(page.Maintenance) != 2 {
			t.Fatalf("unsigned JSON %s did not match the published page: %d, %v", path, code, err)
		}
	}
	if code, body, _ := e.get("/status/first-app/incidents/a1", ""); code != http.StatusOK ||
		!strings.Contains(body, "Service restored before publication") || strings.Contains(body, "Internal-only incident note") {
		t.Fatalf("the signed-out incident did not hold the public-note policy: %d", code)
	}
	if a := owner.post(newStatusPagePath, data); a.code != http.StatusConflict {
		t.Fatalf("replaying the publication replaced the page: %d", a.code)
	}
}

func TestCreateStatusPagePreservesExistingRoutes(t *testing.T) {
	e := statusPageCreator(t)
	owner := e.owner()
	ctx := context.Background()
	put := func(kind, name, raw string) {
		t.Helper()
		if _, err := e.cdb.Put(ctx, configdb.Actor{Name: "fixture", Via: "cli"}, kind, name, json.RawMessage(raw), ""); err != nil {
			t.Fatal(err)
		}
	}
	put(configdb.KindTargets, "all-http", `[{"targets":["https://example.com"],"labels":{"module":"http_2xx","service":"first-app"}}]`)
	const page = `{"title":"Existing public page","host":"uptime.example.com","components":[{"name":"root","title":"Existing component","services":["first-app"]}]}`
	put(configdb.KindStatus, "new", page)
	if a := owner.get("/console/status-pages/new"); a.code != http.StatusOK || !strings.Contains(a.body, "Existing public page") {
		t.Fatalf("the new form shadowed a pre-existing page named new: %d", a.code)
	}
	if a := owner.get("/console/status-pages"); a.code != http.StatusOK || strings.Contains(a.body, newStatusPagePath) ||
		!strings.Contains(a.body, "Mark the current page as the default") {
		t.Fatalf("the list offered creation without a safe default or gave no direction: %d", a.code)
	}
	if a := owner.get(newStatusPagePath); a.code != http.StatusConflict {
		t.Fatalf("creation opened while the fallback default could move: %d", a.code)
	}
	put(configdb.KindStatus, "new", strings.Replace(page, `"host":`, `"default":true,"host":`, 1))
	if a := owner.get("/console/status-pages"); a.code != http.StatusOK || !strings.Contains(a.body, newStatusPagePath) {
		t.Fatalf("creation was unavailable beside an explicit default: %d", a.code)
	}
	old, _ := e.cdb.Catalog().StatusPage("new")
	oldBytes, _ := json.Marshal(old)
	before, _, _ := e.get("/", "")
	data := url.Values{"name": {"earlier-page"}, "title": {"New named page"}, "service": {"first-app"},
		"intent": {"preview"}, "version": {strconv.FormatInt(e.cdb.Version(), 10)}}
	a := owner.post(newStatusPagePath, data)
	data.Set("confirmation", statusPageConfirmation(t, a, http.StatusOK))
	if strings.Contains(a.body, "will also open at / and /status") {
		t.Fatal("preview offered the existing default routes to the new page")
	}
	data.Set("intent", "publish")
	if a := owner.post(newStatusPagePath, data); a.code != http.StatusSeeOther {
		t.Fatalf("publishing an additional page: %d", a.code)
	}
	got, _ := e.cdb.Catalog().DefaultStatusPage()
	gotBytes, _ := json.Marshal(got)
	if string(gotBytes) != string(oldBytes) {
		t.Fatal("the previous default or its dedicated host definition changed")
	}
	if code, body, _ := e.get("/status", ""); code != http.StatusOK || !strings.Contains(body, "Existing public page") {
		t.Fatalf("the existing /status alias was retargeted: %d", code)
	}
	if code, body, _ := e.get("/", ""); code != before || !strings.Contains(body, "Self-hosted monitoring and SSH access") {
		t.Fatalf("the console host's landing page changed into a status page: %d", code)
	}
	if code, body, _ := e.get("/status/earlier-page", ""); code != http.StatusOK || !strings.Contains(body, "New named page") {
		t.Fatalf("new page did not open at its separate address: %d", code)
	}
	host := e.srv.Hosts(e.mux)
	atHost := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		host.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://uptime.example.com"+path, nil))
		return rec
	}
	if rec := atHost("/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Existing public page") {
		t.Fatalf("switching pages altered the dedicated root: %d", rec.Code)
	}
	if rec := atHost(newStatusPagePath); rec.Code != http.StatusNotFound {
		t.Fatalf("the creation form could be reached through a dedicated public host: %d", rec.Code)
	}
	data.Set("intent", "preview")
	data.Set("name", "new")
	data.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	if a := owner.post(newStatusPagePath, data); a.code != http.StatusConflict || !strings.Contains(a.body, "already uses that ID") {
		t.Fatalf("the create form accepted an existing page ID: %d", a.code)
	}
}
