package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
)

func TestPlanMaintenance(t *testing.T) {
	e := newEnv(t, i18n.EN)
	ctx := context.Background()
	if _, err := e.cdb.Import(ctx, configdb.Actor{Name: "test"},
		configdb.Files{"catalog/catalog.yaml": statusCatalogue, "targets/all-web.yaml": statusTargets}, "", false); err != nil {
		t.Fatal(err)
	}
	pages := status.New(status.Deps{Config: e.cdb, Bridge: e.b, Store: e.st, Now: func() time.Time { return e.now }})
	e.srv.Status = pages
	reader := e.signedIn("reader", "status:read", "alerts:write")
	if a := reader.get("/console/maintenance/new"); a.code != http.StatusForbidden {
		t.Fatalf("on-call reader opening the form: %d", a.code)
	}
	if a := reader.post("/console/maintenance", nil); a.code != http.StatusForbidden {
		t.Fatalf("on-call reader scheduling: %d", a.code)
	}
	w := e.invite(e.owner(), "operator", "operator") // a person's window mutes what it covers
	if a := w.get("/console/maintenance/new?service=api.example.com"); a.code != http.StatusOK ||
		!strings.Contains(a.body, `name="service" value="api.example.com" checked`) {
		t.Fatalf("service is not selected bringing the form up from its monitor page: %d", a.code)
	}
	in := url.Values{
		"title": {"Database upgrade"}, "description": {"Public details"}, "service": {"api.example.com"},
		"start": {e.now.Add(time.Hour).Format(wallTimeFormat)}, "end": {e.now.Add(2 * time.Hour).Format(wallTimeFormat)}, "intent": {"preview"},
	}
	if a := w.do(http.MethodPost, "/console/maintenance", in); a.code != http.StatusForbidden {
		t.Fatalf("preview without CSRF: %d", a.code)
	}
	if a := w.post("/console/maintenance", in); a.code != http.StatusOK ||
		!strings.Contains(a.body, `href="/status/public">Example Status · API &lt;img`) ||
		!strings.Contains(a.body, "Public details") || !strings.Contains(a.body, "Effect on alerts") || e.am.made != 0 {
		t.Fatalf("public preview did not match its affected page or made a silence: %d, %d silences", a.code, e.am.made)
	}
	// A change between preview and publish refreshes the preview without scheduling anything.
	in.Set("intent", "schedule")
	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	if _, err := e.cdb.Put(ctx, configdb.Actor{Name: "test"}, configdb.KindModule, "http_2xx", json.RawMessage(`{"prober":"http"}`), ""); err != nil {
		t.Fatal(err)
	}
	if a := w.post("/console/maintenance", in); a.code != http.StatusConflict || e.am.made != 0 {
		t.Fatalf("stale preview could be published: %d, %d silences", a.code, e.am.made)
	}
	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	a := w.post("/console/maintenance", in)
	if a.code != http.StatusSeeOther || a.header.Get("Location") != "/console/silences?done=maintenance_scheduled" || e.am.made != 1 ||
		!audited(e.st, "operator", "maintenance") || !e.bot.told("operator", "Database upgrade") {
		t.Fatalf("scheduling failed: %d %q, %d silences", a.code, a.header.Get("Location"), e.am.made)
	}
	if a := w.get(a.header.Get("Location")); a.code != http.StatusOK || !strings.Contains(a.body, "Maintenance scheduled") ||
		!strings.Contains(a.body, "Database upgrade") || !strings.Contains(a.body, "Call off") {
		t.Fatalf("maintenance not in the console list: %d", a.code)
	}
	public, err := pages.Page(ctx, "public", "")
	if err != nil || len(public.Maintenance) != 1 || public.Maintenance[0].Title != "Database upgrade" ||
		public.Maintenance[0].Description != "Public details" || public.Maintenance[0].Status != "scheduled" {
		t.Fatalf("scheduled window not on public status page: %+v %v", public, err)
	}
}
