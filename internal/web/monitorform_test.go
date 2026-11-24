package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
)

func TestAddHTTPMonitor(t *testing.T) {
	e := newEnv(t, i18n.EN)
	w := e.signedIn("owner", "status:read", "config:write")
	reader := e.signedIn("reader", "status:read")
	const form = "/console/monitors/add/"
	if a := reader.get(form); a.code != http.StatusForbidden {
		t.Fatalf("reader opening form: %d", a.code)
	}
	if a := reader.post("/console/monitors", nil); a.code != http.StatusForbidden {
		t.Fatalf("reader adding monitor: %d", a.code)
	}
	if a := w.get(form); a.code != http.StatusOK || !strings.Contains(a.body, "Import a GET or HEAD HTTP check profile") {
		t.Fatalf("first visit without modules: %d", a.code)
	}
	for _, m := range []struct {
		name, value string
	}{
		{"http_2xx", `{"prober":"http"}`},
		{"http_post", `{"prober":"http","http":{"method":"POST"}}`},
	} {
		if _, err := e.cdb.Put(context.Background(), configdb.Actor{Name: "test"}, configdb.KindModule, m.name, json.RawMessage(m.value), ""); err != nil {
			t.Fatal(err)
		}
	}
	if a := w.get(form); a.code != http.StatusOK || !strings.Contains(a.body, `value="http_2xx"`) ||
		!strings.Contains(a.body, `value="edge-1"`) || strings.Contains(a.body, `value="http_post"`) {
		t.Fatalf("module and location choices: %d", a.code)
	}

	base := e.cdb.Version()
	in := url.Values{"service": {"test-api"}, "url": {"https://example.com/health"}, "scope": {"edge-1"},
		"module": {"http_2xx"}, "severity": {"warning"}, "version": {strconv.FormatInt(base, 10)}}
	if a := w.do(http.MethodPost, "/console/monitors", in); a.code != http.StatusForbidden || e.cdb.Version() != base {
		t.Fatalf("submission without CSRF changed config: %d", a.code)
	}
	in.Set("url", "https://example.com/health?token=unit-test-credential")
	if a := w.post("/console/monitors", in); a.code != http.StatusBadRequest || strings.Contains(a.body, "unit-test-credential") || e.cdb.Version() != base {
		t.Fatalf("URL with query was accepted or displayed: %d", a.code)
	}
	in.Set("url", "https://example.com/health")
	a := w.post("/console/monitors", in)
	if a.code != http.StatusSeeOther || a.header.Get("Location") != "/console/monitors/test-api?done=monitor_added" {
		t.Fatalf("new monitor: %d %q", a.code, a.header.Get("Location"))
	}
	if a = w.get(a.header.Get("Location")); a.code != http.StatusOK || !strings.Contains(a.body, "Monitor added") {
		t.Fatalf("following the new monitor: %d", a.code)
	}
	groups := e.cdb.Targets().Groups("edge-1-http.test-api.monitor.yaml")
	if len(groups) != 1 || groups[0].Labels["service"] != "test-api" || groups[0].Labels["visibility"] != "internal" ||
		groups[0].Targets[0] != "https://example.com/health" {
		t.Fatalf("target written: %+v", groups)
	}
	if !audited(e.st, "owner", "config_put") || e.cdb.Version() != base+1 {
		t.Fatal("write was not recorded as the owner in configuration history and audit")
	}
	in.Set("service", "second-api")
	if a := w.post("/console/monitors", in); a.code != http.StatusConflict || e.cdb.Version() != base+1 {
		t.Fatalf("stale form: %d", a.code)
	}
	in.Set("version", strconv.FormatInt(base+1, 10))
	in.Set("service", "test-api")
	if a := w.post("/console/monitors", in); a.code != http.StatusConflict || e.cdb.Version() != base+1 {
		t.Fatalf("duplicate service: %d", a.code)
	}
	in.Set("service", "second-api")
	if a := w.post("/console/monitors", in); a.code != http.StatusSeeOther || len(e.cdb.Targets().Groups("edge-1-http.second-api.monitor.yaml")) != 1 {
		t.Fatalf("adding another service: %d", a.code)
	}
	if _, err := e.cdb.Revert(context.Background(), configdb.Actor{Name: "owner", Via: "console"}, base+1, ""); err != nil {
		t.Fatalf("each monitor has independent history: %v", err)
	}
	if len(e.cdb.Targets().Groups("edge-1-http.test-api.monitor.yaml")) != 0 ||
		len(e.cdb.Targets().Groups("edge-1-http.second-api.monitor.yaml")) != 1 {
		t.Fatal("reverting one monitor changed another one")
	}
	path := "/console/monitors/second-api"
	if a := w.get(path + "/edit"); a.code != http.StatusOK || !strings.Contains(a.body, "Edit HTTP monitor") {
		t.Fatalf("opening editor: %d", a.code)
	}
	if a := reader.get(path + "/edit"); a.code != http.StatusForbidden {
		t.Fatalf("reader opening editor: %d", a.code)
	}
	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	in.Set("url", "https://example.com/status")
	in.Set("scope", "all") // a forged form must not change the existing probe scope
	if a := w.post(path+"/edit", in); a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "done=saved") {
		t.Fatalf("editing a monitor: %d", a.code)
	}
	groups = e.cdb.Targets().Groups("edge-1-http.second-api.monitor.yaml")
	if len(groups) != 1 || groups[0].Targets[0] != "https://example.com/status" {
		t.Fatalf("changed monitor: %+v", groups)
	}
	if a := w.post(path+"/remove", in); a.code != http.StatusConflict {
		t.Fatalf("stale removal: %d", a.code)
	}
	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	if a := w.post(path+"/remove", in); a.code != http.StatusSeeOther || len(e.cdb.Targets().Groups("edge-1-http.second-api.monitor.yaml")) != 0 {
		t.Fatalf("removing a monitor: %d", a.code)
	}
}
