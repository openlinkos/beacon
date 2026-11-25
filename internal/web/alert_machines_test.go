package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestIncidentFirstKeySetupKeepsMachineAndEpisode(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	oncall := e.invite(owner, "first-duty", "oncall")
	reader := e.invite(owner, "incident-reader", "viewer")
	g, _ := e.st.Group(e.id)
	g.Alerts[0].Labels = map[string]string{"machine": "edge-1"}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	m, _ := e.st.Machine("edge-1")
	episode := link.IncidentPath(g.ID, g.Episode)
	ref := g.ID + "." + g.Episode
	registration := m.Registration()
	ssh := incidentMachinePath(m.Name, registration, ref)
	account := incidentKeyReturn{Ref: ref, Machine: m.Name, Registration: registration}.accountPath()
	assertLink := func(r answer, path string) {
		t.Helper()
		if r.code != http.StatusOK || !slices.Contains(hrefs(r.body, path), path) {
			t.Fatalf("missing contextual link to %q (status %d)", path, r.code)
		}
	}
	request := func(path string) string {
		path, _, _ = strings.Cut(path, "#")
		return path
	}
	assertLink(oncall.get(episode), ssh)
	assertLink(oncall.get(request(ssh)), account)
	assertLink(oncall.get(request(ssh)), episode)
	if result := reader.get(request(ssh)); result.code != http.StatusOK || strings.Contains(result.body, `id="ssh-connect"`) || strings.Contains(result.body, "Back to incident") {
		t.Fatalf("viewer received the personal SSH hand-off: %d", result.code)
	}
	if code, body, _ := e.get(e.link(episode), ""); code != http.StatusOK || strings.Contains(body, "ssh-connect") {
		t.Fatalf("anonymous alert received the personal SSH hand-off: %d", code)
	}
	assertLink(oncall.get(request(account)), ssh)
	fields := url.Values{"incident": {ref}, "ssh_machine": {m.Name}, "registration": {registration}, "key": {"invalid"}}
	retry := oncall.post("/console/account/keys", fields)
	u, err := url.Parse(retry.header.Get("Location"))
	if err != nil || retry.code != http.StatusSeeOther || u.Query().Get("error") != "bad_key" || u.Query().Get("registration") != registration ||
		u.Query().Get("incident") != ref || u.Fragment != "ssh-keys" {
		t.Fatalf("retry lost its context: %d, %q", retry.code, retry.header.Get("Location"))
	}
	assertLink(oncall.get(u.RequestURI()), ssh)
	fields.Set("key", lockPubKey(t))
	added := oncall.post("/console/account/keys", fields)
	u, err = url.Parse(added.header.Get("Location"))
	if err != nil || added.code != http.StatusSeeOther || u.Query().Get("done") != "key_added" || u.Query().Get("registration") != registration ||
		u.Query().Get("incident") != ref || u.Fragment != "ssh-keys" {
		t.Fatalf("adding a key lost its context: %d, %q", added.code, added.header.Get("Location"))
	}
	assertLink(oncall.get(u.RequestURI()), ssh)

	hook := alertmanager.Webhook{GroupKey: groupKey, Status: "resolved", CommonLabels: g.Labels}
	e.now = e.now.Add(time.Minute)
	if err := e.b.HandleWebhook(context.Background(), hook); err != nil {
		t.Fatal(err)
	}
	e.now, hook.Status = e.now.Add(time.Minute), "firing"
	if err := e.b.HandleWebhook(context.Background(), hook); err != nil {
		t.Fatal(err)
	}
	if next, _ := e.st.Group(e.id); next.Episode == g.Episode {
		t.Fatal("fixture did not refire")
	}
	assertLink(oncall.get(request(ssh)), episode)
	if archived := oncall.get(episode); archived.code != http.StatusSeeOther || !strings.Contains(archived.header.Get("Location"), "/incident-outcomes/") {
		t.Fatalf("old episode does not return the archived outcome: %d", archived.code)
	}
	if err := e.st.DeleteMachine(m.Name); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PutMachine(m); err != nil {
		t.Fatal(err)
	}
	assertLink(oncall.get(request(account)), episode)
	if page := oncall.get(request(account)); strings.Contains(page.body, "Back to SSH setup for") ||
		strings.Contains(page.body, "ssh_machine") {
		t.Fatal("key setup offered to connect to a replacement machine")
	}
	if page := oncall.get(request(ssh)); page.code != http.StatusNotFound {
		t.Fatalf("pinned machine link opened a new registration: %d", page.code)
	}
	for _, ref := range []string{"https://other.example/ssh", "//other.example", g.ID + ".invalid"} {
		failed := oncall.post("/console/account/keys", url.Values{"incident": {ref}, "ssh_machine": {m.Name}, "key": {"invalid"}})
		if failed.code != http.StatusSeeOther || failed.header.Get("Location") != "/console/account?error=bad_key" {
			t.Fatalf("the key form accepted an invalid return reference %q", ref)
		}
	}
}

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
	m, _ := e.st.Machine("edge-1")
	ssh := incidentMachinePath(m.Name, m.Registration(), g.ID+"."+g.Episode)
	if a := oncall.get(path); a.code != http.StatusOK || !strings.Contains(a.body, `href="`+html.EscapeString(ssh)+`"`) ||
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
	m, _ := e.st.Machine("edge-2")
	ssh := incidentMachinePath(m.Name, m.Registration(), g.ID+"."+g.Episode)
	if a := oncall.get(path); a.code != http.StatusOK || !strings.Contains(a.body, `href="`+html.EscapeString(ssh)+`"`) ||
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
