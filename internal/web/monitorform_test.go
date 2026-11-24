package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
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
	if a := w.get(path + "/edit"); a.code != http.StatusOK || !strings.Contains(a.body, "Edit HTTP monitor") ||
		!strings.Contains(a.body, `id="monitor-scope"`) || !strings.Contains(a.body, `value="edge-1" selected`) {
		t.Fatalf("opening editor: %d", a.code)
	}
	if a := reader.get(path + "/edit"); a.code != http.StatusForbidden {
		t.Fatalf("reader opening editor: %d", a.code)
	}
	version := e.cdb.Version()
	in.Set("version", strconv.FormatInt(version, 10))
	in.Set("url", "https://example.com/status")
	in.Set("scope", "all")
	in.Set("service", "rename-from-form")
	if a := w.post(path+"/edit", in); a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "done=saved") {
		t.Fatalf("editing a monitor: %d", a.code)
	}
	groups = e.cdb.Targets().Groups("all-http.second-api.monitor.yaml")
	if len(groups) != 1 || groups[0].Targets[0] != "https://example.com/status" || groups[0].Labels["service"] != "second-api" ||
		len(e.cdb.Targets().Groups("edge-1-http.second-api.monitor.yaml")) != 0 {
		t.Fatalf("changed monitor: %+v", groups)
	}
	move, err := e.cdb.Change(version + 1)
	if err != nil || e.cdb.Version() != version+1 || len(move.Items) != 2 ||
		!slices.ContainsFunc(move.Items, func(i store.ConfigItem) bool {
			return i.Key == "edge-1-http.second-api.monitor" && i.Op == "removed" && i.ConsoleBefore
		}) ||
		!slices.ContainsFunc(move.Items, func(i store.ConfigItem) bool {
			return i.Key == "all-http.second-api.monitor" && i.Op == "added" && i.ConsoleAfter
		}) {
		t.Fatalf("scope move should be one target-set change with two items: %+v %v", move, err)
	}
	if a := w.post(path+"/remove", in); a.code != http.StatusConflict {
		t.Fatalf("stale removal: %d", a.code)
	}
	if _, err := e.cdb.Revert(context.Background(), configdb.Actor{Name: "owner", Via: "console"}, move.Version, ""); err != nil {
		t.Fatalf("reverting location change: %v", err)
	}
	e.cdb.Reload()
	if len(e.cdb.Targets().Groups("all-http.second-api.monitor.yaml")) != 0 ||
		len(e.cdb.Targets().Groups("edge-1-http.second-api.monitor.yaml")) != 1 {
		t.Fatal("reverting and reloading did not restore the old scope")
	}
	if a := w.get(path + "/edit"); a.code != http.StatusOK || !strings.Contains(a.body, `value="edge-1" selected`) {
		t.Fatalf("revert did not restore console ownership or selected option: %d", a.code)
	}
	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	if a := w.post(path+"/remove", in); a.code != http.StatusSeeOther || len(e.cdb.Targets().Groups("edge-1-http.second-api.monitor.yaml")) != 0 {
		t.Fatalf("removing a monitor: %d", a.code)
	}
}

func TestHTTPMonitorProvenanceAndDestinationConflict(t *testing.T) {
	e := newEnv(t, i18n.EN)
	w := e.signedIn("owner", "status:read", "config:write")
	ctx := context.Background()
	actor := configdb.Actor{Name: "test", Via: "cli"}
	if _, err := e.cdb.Put(ctx, actor, configdb.KindModule, "http_2xx", json.RawMessage(`{"prober":"http"}`), ""); err != nil {
		t.Fatal(err)
	}
	const managedSet = "edge-1-http.legacy-api.monitor"
	const importedSet = "all-http.lookalike.monitor"
	const lookalike = `[{"targets":["https://example.com/health"],"labels":{"module":"http_2xx","service":"lookalike","severity":"warning","visibility":"internal"}}]`
	const legacy = `[{"targets":["https://example.com/health"],"labels":{"module":"http_2xx","service":"legacy-api","severity":"warning","visibility":"internal"}}]`
	// Seed a pre-upgrade console write. It has no durable ownership marker yet.
	if _, err := e.st.CommitConfig(store.ConfigChange{Actor: "owner", Via: "console", Action: "put", Note: "Add HTTP monitor legacy-api",
		Items: []store.ConfigItem{{Kind: configdb.KindTargets, Key: managedSet, Op: "added", After: json.RawMessage(legacy)}}},
		[]store.ConfigWrite{{Key: "targets/" + managedSet, Value: []byte(legacy)}}); err != nil {
		t.Fatal(err)
	}
	e.cdb.Reload()
	files, _, err := e.cdb.Export()
	if err != nil {
		t.Fatal(err)
	}
	// JSON is also YAML. This file has precisely the old console shape, but came from an import.
	files["targets/"+importedSet+".yaml"] = lookalike
	// This file gives the editor a destination collision, while it monitors a different service.
	const collided = "all-http.legacy-api.monitor"
	files["targets/"+collided+".yaml"] = strings.ReplaceAll(lookalike, "lookalike", "other-api")
	if _, err := e.cdb.Import(ctx, actor, files, "import lookalikes", false); err != nil {
		t.Fatal(err)
	}
	// Before the upgrade a lookalike could pass the editor's shape check. Even an old edit with
	// known wording cannot claim an imported target.
	edited := strings.ReplaceAll(lookalike, "/health", "/status")
	if _, err := e.st.CommitConfig(store.ConfigChange{Actor: "owner", Via: "console", Action: "put", Note: "Edit HTTP monitor lookalike",
		Items: []store.ConfigItem{{Kind: configdb.KindTargets, Key: importedSet, Op: "changed",
			Before: json.RawMessage(lookalike), After: json.RawMessage(edited)}}},
		[]store.ConfigWrite{{Key: "targets/" + importedSet, Was: []byte(lookalike), Value: []byte(edited)}}); err != nil {
		t.Fatal(err)
	}
	e.cdb.Reload()
	if a := w.get("/console/monitors/lookalike"); a.code != http.StatusOK || strings.Contains(a.body, "Edit HTTP monitor") {
		t.Fatalf("imported target is not editable but its results should be visible: %d", a.code)
	}
	if a := w.get("/console/monitors/lookalike/edit"); a.code != http.StatusNotFound {
		t.Fatalf("permitted direct edit of imported target: %d", a.code)
	}
	if a := w.post("/console/monitors/lookalike/remove", nil); a.code != http.StatusNotFound {
		t.Fatalf("permitted direct removal of imported target: %d", a.code)
	}
	if a := w.get("/console/monitors/legacy-api/edit"); a.code != http.StatusOK || !strings.Contains(a.body, `value="edge-1" selected`) {
		t.Fatalf("owned legacy monitor did not keep its form or selection: %d", a.code)
	}
	version := e.cdb.Version()
	in := url.Values{"version": {strconv.FormatInt(version, 10)}, "scope": {"all"}, "module": {"http_2xx"},
		"severity": {"warning"}, "url": {"https://example.com/status"}}
	if a := w.post("/console/monitors/legacy-api/edit", in); a.code != http.StatusConflict ||
		!strings.Contains(a.body, "already uses this name and scope") || e.cdb.Version() != version {
		t.Fatalf("move overwrote another config: %d", a.code)
	}
	got := e.cdb.Targets().Groups(collided + ".yaml")
	if len(got) != 1 || got[0].Labels["service"] != "other-api" || len(e.cdb.Targets().Groups(managedSet+".yaml")) != 1 {
		t.Fatalf("blocked move changed the monitors: %+v", got)
	}
}
