package web

import (
	"context"
	"encoding/json"
	"net/http"
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

func TestPublicComponentReviewsExistingMaintenance(t *testing.T) {
	e := newEnv(t, i18n.EN)
	ctx := context.Background()
	e.srv.Status = status.New(status.Deps{Config: e.cdb, Bridge: e.b, Store: e.st, Now: func() time.Time { return e.now }, PublicURL: publicURL})
	e.mux = http.NewServeMux()
	e.srv.Register(e.mux)
	owner := e.owner()
	actor := configdb.Actor{Name: "fixture", Via: "cli"}
	put := func(kind, name, raw string) {
		t.Helper()
		if _, err := e.cdb.Put(ctx, actor, kind, name, json.RawMessage(raw), ""); err != nil {
			t.Fatal(err)
		}
	}
	put(configdb.KindModule, "http_2xx", `{"prober":"http"}`)
	monitor := url.Values{"service": {"first-app"}, "url": {"https://example.com/health"}, "scope": {"all"},
		"module": {"http_2xx"}, "severity": {"warning"}, "version": {strconv.FormatInt(e.cdb.Version(), 10)}}
	if a := owner.post("/console/monitors", monitor); a.code != http.StatusSeeOther {
		t.Fatalf("owner's HTTP monitor: %d", a.code)
	}
	put(configdb.KindTargets, "all-public", `[{"targets":["https://example.org/health"],"labels":{"module":"http_2xx","service":"public-app"}}]`)
	put(configdb.KindStatus, "public", `{"title":"Fixture Status","components":[{"name":"already-public","title":"Already Public","services":["public-app"]}]}`)
	const (
		previewPath   = "/console/status-pages/public/components/add"
		publicPath    = "/status/public"
		internalTitle = "Fixture maintenance title"
		internalDesc  = "Fixture public text <example>"
		overlapTitle  = "Already visible through another service"
		legacyTitle   = "Legacy maintenance silence"
		arrivalTitle  = "Maintenance scheduled during review"
		expiredTitle  = "Expired announcement beyond retention"
	)
	if a := owner.get("/console/maintenance/new?service=first-app"); a.code != http.StatusOK ||
		!strings.Contains(a.body, `name="service" value="first-app" checked`) {
		t.Fatalf("the monitor did not preselect its service for maintenance: %d", a.code)
	}
	start, end := e.now.Add(time.Hour), e.now.Add(2*time.Hour)
	in := url.Values{"title": {internalTitle}, "description": {internalDesc}, "service": {"first-app"}, "intent": {"preview"},
		"start": {start.Format(wallTimeFormat)}, "end": {end.Format(wallTimeFormat)}}
	if a := owner.post("/console/maintenance", in); a.code != http.StatusOK ||
		!strings.Contains(a.body, "No current public status page shows these services.") {
		t.Fatalf("maintenance preview had the wrong public scope at scheduling: %d", a.code)
	}
	in.Set("intent", "schedule")
	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	if a := owner.post("/console/maintenance", in); a.code != http.StatusSeeOther || e.am.made != 1 {
		t.Fatalf("scheduling did not retain its silence: %d", a.code)
	}
	schedule := func(title string, services ...string) {
		t.Helper()
		_, err := e.b.ScheduleMaintenance(ctx, bridge.Actor{Name: "fixture", Via: "api"}, bridge.MaintenanceInput{
			Title: title, Services: services, StartsAt: start, EndsAt: end,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	schedule(overlapTitle, "first-app", "public-app")
	if err := e.st.PutSilence(store.SilenceRecord{ID: "legacy", Comment: "maintenance: " + legacyTitle,
		Matchers: []store.Matcher{{Name: "service", Value: "first-app", IsEqual: true}}, StartsAt: start, EndsAt: end}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PutMaintenance(store.Maintenance{ID: "old-window", Title: expiredTitle, Services: []string{"first-app"},
		StartsAt: e.now.AddDate(0, 0, -45), EndsAt: e.now.AddDate(0, 0, -44)}, e.now.Add(-store.KeepStatus)); err != nil {
		t.Fatal(err)
	}
	initial := func() {
		t.Helper()
		for _, path := range []string{publicPath, publicPath + ".json", publicPath + "/feed.atom"} {
			code, body, _ := e.get(path, "")
			if code != http.StatusOK || !strings.Contains(body, overlapTitle) || strings.Contains(body, internalTitle) ||
				strings.Contains(body, legacyTitle) || strings.Contains(body, arrivalTitle) {
				t.Fatalf("%s: preview or conflict changed the live announcement list: %d", path, code)
			}
		}
	}
	initial()
	_, configOnly, err := e.st.CreateToken("configuration-only", []string{"config:write"}, time.Hour, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := e.st.CreateConsoleSession(configOnly, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	getter := e.browser()
	getter.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: cookie}
	if a := getter.get(previewPath); a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "sign-in") || strings.Contains(a.body, internalTitle) {
		t.Fatalf("config-only session can reach announcement preview: %d", a.code)
	}

	version := e.cdb.Version()
	data := url.Values{"component": {"first-app"}, "title": {"First App"}, "service": {"first-app"},
		"intent": {"preview"}, "version": {strconv.FormatInt(version, 10)}}
	a := owner.post(previewPath, data)
	fingerprint := regexp.MustCompile(`name="confirmation" value="([0-9a-f]{64})"`)
	form := fingerprint.FindStringSubmatch(a.body)
	if a.code != http.StatusOK || len(form) != 2 || !strings.Contains(a.body, "Maintenance announcements that will become public") ||
		!strings.Contains(a.body, internalTitle) || !strings.Contains(a.body, "Fixture public text &lt;example&gt;") ||
		!strings.Contains(a.body, legacyTitle) || strings.Contains(a.body, overlapTitle) || strings.Contains(a.body, expiredTitle) {
		t.Fatalf("the reviewed entries should be the exact upcoming and retained public delta: %d", a.code)
	}
	initial()
	data.Set("intent", "publish")
	if a = owner.post(previewPath, data); a.code != http.StatusConflict || e.cdb.Version() != version {
		t.Fatalf("publication could skip the announcement preview: %d", a.code)
	}
	data.Set("confirmation", form[1])
	schedule(arrivalTitle, "first-app")
	a = owner.post(previewPath, data)
	form = fingerprint.FindStringSubmatch(a.body)
	if a.code != http.StatusConflict || e.cdb.Version() != version || len(form) != 2 ||
		!strings.Contains(a.body, arrivalTitle) {
		t.Fatalf("the confirmation accepted a stale preview: %d", a.code)
	}
	initial()
	data.Set("confirmation", form[1])
	if a = owner.post(previewPath, data); a.code != http.StatusSeeOther || e.cdb.Version() != version+1 {
		t.Fatalf("owner publishing after reviewing the latest announcements: %d", a.code)
	}
	code, body, _ := e.get(publicPath+".json", "")
	var page status.Page
	if err := json.Unmarshal([]byte(body), &page); err != nil || code != http.StatusOK || len(page.Maintenance) != 4 {
		t.Fatalf("public page had the wrong set of announcements: status %d error %v count %d", code, err, len(page.Maintenance))
	}
	for _, window := range page.Maintenance {
		if window.Title == internalTitle && window.Description == internalDesc && window.Status == "scheduled" {
			return
		}
	}
	t.Fatal("the live public page differed from the text or status the owner reviewed")
}
