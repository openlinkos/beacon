package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestAlertLinksOnlyExplicitEnrolledMachines(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	oncall := e.invite(owner, "duty", "oncall")
	viewer := e.invite(owner, "info", "viewer")
	path := link.AlertPath(e.id)
	machine := "/console/machines/edge-1"
	// The fixture already names a host and has a related session on it. Neither is a navigation
	// target unless the alert declares a machine.
	if a := oncall.get(path); a.code != http.StatusOK || !strings.Contains(a.body, "No machine is linked to this alert.") || strings.Contains(a.body, machine) {
		t.Fatalf("a host or related session was offered as an explicitly linked machine: %d", a.code)
	}
	g, _ := e.st.Group(e.id)
	g.Alerts[0].Labels = map[string]string{"machine": "edge-1"}
	g.Common["machine"] = "not-enrolled"
	g.Common["location"] = "edge-2"
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if a := oncall.get(path); a.code != http.StatusOK || !strings.Contains(a.body, `href="`+machine+`#ssh-connect"`) ||
		strings.Contains(a.body, `href="/console/machines/edge-2`) || strings.Contains(a.body, `href="/console/machines/not-enrolled`) {
		t.Fatalf("on-call should have the SSH hand-off only for the enrolled declared machine: %d", a.code)
	}
	if a := viewer.get(path); a.code != http.StatusOK || !strings.Contains(a.body, `href="`+machine+`"`) || strings.Contains(a.body, "#ssh-connect") {
		t.Fatalf("viewer should only have the machine details link: %d", a.code)
	}
	for _, tc := range []struct{ url, token string }{{e.link(path), ""}, {path, e.token("status-client", "status:read")}} {
		if code, body, _ := e.get(tc.url, tc.token); code != http.StatusOK || strings.Contains(body, "Linked machines") || strings.Contains(body, machine) {
			t.Errorf("a read-only link or bearer request got console navigation: %d", code)
		}
	}
	delete(g.Alerts[0].Labels, "machine")
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if a := oncall.get(path); a.code != http.StatusOK || !strings.Contains(a.body, "This alert links to machines that are not enrolled.") || strings.Contains(a.body, machine) {
		t.Errorf("the unenrolled machine did not produce a clear empty state: %d", a.code)
	}
}

func TestAlertLinksCatalogueHeartbeatMachine(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	oncall := e.invite(owner, "duty", "oncall")
	path := link.AlertPath(e.id)
	e.srv.Bridge = bridge.New(bridge.Deps{Config: &config.Config{Location: time.UTC}, Store: e.st, Pages: e.srv.Pages, Catalog: e.cdb.Catalog,
		Now: func() time.Time { return e.now }})
	_, err := e.cdb.Put(context.Background(), configdb.Actor{Name: "alice", Via: "api"}, "heartbeats", "job-one",
		json.RawMessage(`{"max_age":"2h","severity":"warning","machine":"edge-2"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := e.st.Group(e.id)
	g.Common["heartbeat"] = "job-one"
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if a := oncall.get(path); a.code != http.StatusOK || !strings.Contains(a.body, `href="/console/machines/edge-2#ssh-connect"`) ||
		strings.Contains(a.body, `href="/console/machines/edge-1`) {
		t.Fatalf("the heartbeat should link its configured machine: %d", a.code)
	}
	e.srv.SSH = nil
	if a := owner.get(path); a.code != http.StatusOK || !strings.Contains(a.body, "The SSH channel is off.") || strings.Contains(a.body, `href="/console/machines/edge-2`) {
		t.Errorf("a disabled channel should explain the unavailable link: %d", a.code)
	}
	found := oncall.post(path+"/notes", incidentForm(t, oncall.get(path), url.Values{"text": {"Shift handover"}}))
	if found.code != http.StatusSeeOther || !strings.Contains(oncall.get(path).body, "Shift handover") {
		t.Fatal("recording a handover must work with the SSH channel disabled")
	}
}
