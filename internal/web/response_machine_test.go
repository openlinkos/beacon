package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
)

const importedResponseService = "checkout-example-com"

func seedImportedResponseMonitor(t *testing.T, e *env) {
	t.Helper()
	actor := configdb.Actor{Name: "maintainer", Via: "cli"}
	if _, err := e.cdb.Put(t.Context(), actor, configdb.KindModule, "http_2xx", json.RawMessage(`{"prober":"http"}`), ""); err != nil {
		t.Fatal(err)
	}
	const grouped = `[
  {"targets":["https://checkout.example.com/health", "https://checkout.example.com/ready"],
   "labels":{"service":"checkout-example-com","module":"http_2xx","severity":"critical","failure_domain":"blue","team":"checkout"}},
  {"targets":["https://green.example.com/ready"],
   "labels":{"service":"checkout-example-com","module":"http_2xx","severity":"critical","failure_domain":"green","team":"checkout"}}
]`
	if _, err := e.cdb.Put(t.Context(), actor, configdb.KindTargets, "all-checkout", json.RawMessage(grouped), "Import checkout monitors"); err != nil {
		t.Fatal(err)
	}
	const overseas = `[{"targets":["https://overseas.example.com/ready"],
  "labels":{"service":"checkout-example-com","module":"http_2xx","severity":"critical","failure_domain":"blue"}}]`
	if _, err := e.cdb.Put(t.Context(), actor, configdb.KindTargets, "overseas-checkout", json.RawMessage(overseas), "Import overseas monitor"); err != nil {
		t.Fatal(err)
	}
}

func TestImportedMonitorResponseMachineForm(t *testing.T) {
	e := newEnv(t, i18n.EN)
	seedImportedResponseMonitor(t, e)
	owner := e.owner()
	readOnly := e.invite(owner, "reader", "viewer")
	legacyOwner := e.signedIn("legacy-owner", "status:read", "config:write", "machines:write")
	const set = "all-checkout"
	detail := monitorDetailPath(importedResponseService)
	path := responseMachinePath(importedResponseService, set, 0)
	oldVersion := strconv.FormatInt(e.cdb.Version(), 10)
	formPath := path + "?version=" + oldVersion
	const oneEndpoint = "https://checkout.example.com/ready"
	const otherEndpoint = "https://green.example.com/ready"
	if seen := owner.get(detail); seen.code != http.StatusOK || !strings.Contains(seen.body, formPath) ||
		!strings.Contains(seen.body, otherEndpoint) || strings.Contains(seen.body, "Edit HTTP monitor") {
		t.Fatal("imported monitor detail did not distinguish its group actions from routine monitor editing")
	}
	if seen := readOnly.get(detail); seen.code != http.StatusOK || strings.Contains(seen.body, path) {
		t.Fatal("read-only monitor detail offered the response machine editor")
	}
	if a := e.browser().get(formPath); a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "/console/sign-in?") {
		t.Fatal("the machine selection form opened without an authenticated account")
	}
	for _, b := range []*browser{readOnly, legacyOwner} {
		if b.get(formPath).code != http.StatusForbidden || b.post(path, url.Values{"version": {oldVersion}, "machine": {""}}).code != http.StatusForbidden {
			t.Fatal("a viewer or standalone token could change the designation")
		}
	}
	if editor := owner.get(formPath); editor.code != http.StatusOK || !strings.Contains(editor.body, oneEndpoint) || strings.Contains(editor.body, otherEndpoint) {
		t.Fatalf("the editor displayed the wrong target group (status %d)", editor.code)
	}
	before := e.cdb.Targets().For("edge-1", "overseas", []string{"edge-1", "edge-2"})
	selection := url.Values{"version": {oldVersion}, "machine": {"edge-1/expired"}}
	if bad := owner.post(path, selection); bad.code != http.StatusBadRequest {
		t.Fatalf("the form accepted a replaced registration (status %d)", bad.code)
	}
	m, _ := e.st.Machine("edge-1")
	selection.Set("machine", m.Name+"/"+m.Registration())
	if saved := owner.post(path, selection); saved.code != http.StatusSeeOther || saved.header.Get("Location") != detail+"?done=saved" {
		t.Fatalf("save status %d, redirect %q", saved.code, saved.header.Get("Location"))
	}
	st := e.cdb.State()
	gs := st.Targets.Groups(set + ".yaml")
	if gs[0].Machine == nil || gs[0].Machine.Registration != m.Registration() || gs[1].Machine != nil || st.ConsoleTarget(set) {
		t.Fatalf("designation did not stay on the selected imported group: %+v", gs)
	}
	after := st.Targets.For("edge-1", "overseas", []string{"edge-1", "edge-2"})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("the designation changed probe targets or labels")
	}
	selection.Set("machine", "")
	if a := owner.post(path, selection); a.code != http.StatusConflict || strings.Contains(a.body, `name="machine"`) || !strings.Contains(a.body, "Back to monitor") {
		t.Fatal("a stale form was not rejected with a clear route back to the current groups")
	}
	if a := owner.get(formPath); a.code != http.StatusConflict || strings.Contains(a.body, `name="machine"`) {
		t.Fatal("an old detail link could open an editor at a shifted group position")
	}
	selection.Set("version", strconv.FormatInt(st.Version, 10))
	if a := owner.post(path, selection); a.code != http.StatusSeeOther || e.cdb.Targets().Groups(set + ".yaml")[0].Machine != nil {
		t.Fatal("could not clear the designation or it left the selected target group behind")
	}
}
