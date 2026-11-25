package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestServiceOutageDesignatedMachine(t *testing.T) {
	e := newEnv(t, i18n.EN)
	ctx := context.Background()
	owner := e.owner()
	oncall := e.invite(owner, "duty", "oncall")
	viewer := e.invite(owner, "reader", "viewer")
	addr, _ := lockSSHServer(t)
	a := owner.post("/console/machines", url.Values{"name": {"app-1"}, "address": {addr}, "user": {"beacon"}})
	if a.code != http.StatusCreated {
		t.Fatalf("enrolling a machine from the console: %d", a.code)
	}
	m, _ := e.st.Machine("app-1")
	pick := "app-1/" + m.Registration()
	if _, err := e.cdb.Put(ctx, configdb.Actor{Name: "alice", Via: "cli"}, configdb.KindModule, "http_2xx",
		json.RawMessage(`{"prober":"http"}`), ""); err != nil {
		t.Fatal(err)
	}
	if a = owner.get("/console/monitors/add/"); a.code != http.StatusOK ||
		!strings.Contains(a.body, `value="`+html.EscapeString(pick)+`">app-1</option>`) {
		t.Fatalf("the enrolled machine is missing from the optional selection: %d", a.code)
	}

	cfgWriter := e.signedIn("config-token", "status:read", "config:write")
	const service = "first-service"
	in := url.Values{"service": {service}, "url": {"https://example.com/health"}, "scope": {"all"},
		"module": {"http_2xx"}, "severity": {"warning"}, "version": {strconv.FormatInt(e.cdb.Version(), 10)}, "machine": {pick}}
	if a = cfgWriter.post("/console/monitors", in); a.code != http.StatusForbidden || !strings.Contains(a.body, "You do not have permission to change the designated machine.") {
		t.Fatalf("config access independent of the system's machine permissions: %d", a.code)
	}
	in.Del("csrf")
	in.Set("machine", "app-1/stale-enrolment")
	if a = owner.post("/console/monitors", in); a.code != http.StatusBadRequest {
		t.Fatalf("accepted a stale registration: %d", a.code)
	}
	in.Set("machine", pick)
	if a = owner.post("/console/monitors", in); a.code != http.StatusSeeOther {
		t.Fatalf("creating the monitor with the current machine: %d", a.code)
	}
	if a = owner.get("/console/monitors/" + service + "/edit"); a.code != http.StatusOK ||
		!strings.Contains(a.body, `value="`+html.EscapeString(pick)+`" selected`) {
		t.Fatalf("the edit did not retain the explicit choice: %d", a.code)
	}
	for _, group := range e.cdb.Targets().For("edge-1", "overseas", []string{"edge-1", "edge-2"}) {
		if group.Labels["service"] == service && (group.Machine != nil || len(group.Labels) != 4) {
			t.Fatal("the designation changed what probes receive")
		}
	}

	e.srv.Bridge = bridge.New(bridge.Deps{Store: e.st, Config: &config.Config{Location: time.UTC}, Pages: e.srv.Pages, Bot: e.bot,
		MonitorMachine: e.cdb.MonitorMachine, Now: func() time.Time { return e.now }})
	rt := router.New(router.Config{GroupBy: []string{"alertname", "service"}}, e.st, e.srv.Bridge, func() time.Time { return e.now }, nil)
	post := func(labels map[string]string, starts, ends time.Time) {
		t.Helper()
		if err := rt.Ingest([]router.Postable{{Labels: labels, StartsAt: starts, EndsAt: ends}}); err != nil {
			t.Fatal(err)
		}
		rt.Flush(ctx)
	}
	current := func() store.Group {
		t.Helper()
		for _, g := range e.st.Groups() {
			if g.Labels["service"] == service {
				return g
			}
		}
		t.Fatal("the reference service outage did not reach the console")
		return store.Group{}
	}
	// ServiceDown emits service, severity and an optional failure domain after a quorum of locations.
	labels := map[string]string{"alertname": "ServiceDown", "service": service, "severity": "warning"}
	e.now = e.now.Add(time.Minute)
	start := e.now
	post(labels, start, start.Add(10*time.Minute))
	g := current()
	path := link.AlertPath(g.ID)
	linkRe := regexp.MustCompile(`href="(/console/machines/app-1\?incident=[^"#]+&amp;registration=[^"#]+)#ssh-connect"`)
	a = oncall.get(path)
	sshLink := linkRe.FindStringSubmatch(a.body)
	if a.code != http.StatusOK || !strings.Contains(a.body, "Designated service machines") || len(sshLink) != 2 {
		t.Fatalf("signed-in on-call member did not get the personal SSH link: %d", a.code)
	}
	machinePath := html.UnescapeString(sshLink[1])
	if a = oncall.get(machinePath); a.code != http.StatusOK || !strings.Contains(a.body, "#ssh-keys") ||
		!strings.Contains(a.body, "ssh_machine=app-1") {
		t.Fatalf("following the machine link did not continue personal SSH setup: %d", a.code)
	}
	if a = viewer.get(path); a.code != http.StatusOK || linkRe.MatchString(a.body) {
		t.Fatalf("the viewer received someone else's SSH action: %d", a.code)
	}
	if code, body, _ := e.get(e.link(path), ""); code != http.StatusOK || strings.Contains(body, "Designated service machines") {
		t.Fatalf("a signed alert link revealed console navigation: %d", code)
	}

	session := func(started time.Time, suffix, registration string) string {
		t.Helper()
		id := started.Format("20060102-150405") + "-" + suffix
		if err := e.st.PutSession(store.SessionRecord{ID: id, Agent: "duty", Via: "ssh", Machine: m.Name,
			MachineRegistration: registration, Kind: "exec", Started: started, Ended: e.now}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	good := session(e.now, "aaaaaa", m.Registration())
	previous := session(m.Added.Add(-time.Minute), "bbbbbb", "")
	unknown := session(e.now, "cccccc", "")
	wrong := session(e.now, "dddddd", "different-registration")
	beforeEnrolment := session(m.Added.Add(-time.Second), "eeeeee", m.Registration())
	replay := link.SessionPath(good)
	if a = oncall.get(path); !strings.Contains(a.body, `href="`+replay+`"`) ||
		!strings.Contains(a.body, "1 sessions on 1 machines, 1 commands") {
		t.Fatal("the personal session did not appear with the normal authenticated replay link")
	}
	for _, id := range []string{previous, unknown, wrong, beforeEnrolment} {
		if strings.Contains(a.body, link.SessionPath(id)) {
			t.Fatalf("matched a session without exact registration and start: %s", id)
		}
	}
	if got := oncall.get(replay); got.code != http.StatusOK || !strings.Contains(got.body, "duty on app-1") {
		t.Fatalf("following the replay link did not open the attributable session: %d", got.code)
	}
	if code, _, _ := e.get(replay, ""); code == http.StatusOK {
		t.Fatal("a newly listed replay could be opened without its own permissions")
	}
	for _, access := range []struct{ path, token string }{{e.link(path), ""}, {path, e.token("incident-api", "status:read", "sessions:read")}} {
		if code, body, _ := e.get(access.path, access.token); code != http.StatusOK || strings.Contains(body, replay) {
			t.Fatalf("designated recording was disclosed on a bearer or signed alert view: %d", code)
		}
	}
	evidence, err := e.srv.Bridge.Evidence(ctx, g.ID, "")
	if err != nil || len(evidence.Done.Sessions) != 0 {
		t.Fatalf("the private recording was added to the existing evidence API: %v", err)
	}

	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	in.Set("machine", "")
	if a = owner.post("/console/monitors/"+service+"/edit", in); a.code != http.StatusSeeOther {
		t.Fatalf("clearing the association in monitor settings: %d", a.code)
	}
	for _, ends := range []time.Time{e.now.Add(10 * time.Minute), e.now} {
		post(labels, start, ends)
		if a = oncall.get(path); !linkRe.MatchString(a.body) {
			t.Fatal("a current or resolved incident followed a later configuration edit")
		}
	}
	if err := e.st.DeleteMachine(m.Name); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PutMachine(m); err != nil {
		t.Fatal(err)
	}
	if a = oncall.get(path); a.code != http.StatusOK || !strings.Contains(a.body, "A designated machine registration is no longer available.") ||
		strings.Contains(a.body, "/console/machines/app-1") || strings.Contains(a.body, replay) {
		t.Fatalf("the old alert linked to a new enrolment of the same name: %d", a.code)
	}
	if a = oncall.get(machinePath); a.code != http.StatusNotFound {
		t.Fatalf("a previously rendered link opened a different registration: %d", a.code)
	}
}

func TestMonitorMachineSnapshotAfterImport(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	m, _ := e.st.Machine("edge-1")
	ctx := context.Background()
	actor := configdb.Actor{Name: "alice", Via: "cli"}
	if _, err := e.cdb.Put(ctx, actor, configdb.KindModule, "http_2xx", json.RawMessage(`{"prober":"http"}`), ""); err != nil {
		t.Fatal(err)
	}
	const svc = "imported-designation"
	in := url.Values{"service": {svc}, "url": {"https://example.com/health"}, "scope": {"all"},
		"module": {"http_2xx"}, "severity": {"critical"}, "version": {strconv.FormatInt(e.cdb.Version(), 10)},
		"machine": {"edge-1/" + m.Registration()}}
	if a := owner.post("/console/monitors", in); a.code != http.StatusSeeOther {
		t.Fatalf("creating the monitor: %d", a.code)
	}
	files, _, err := e.cdb.Export()
	if err != nil {
		t.Fatal(err)
	}
	originally := e.now.Add(time.Second)
	e.now = e.now.Add(time.Minute)
	in.Set("version", strconv.FormatInt(e.cdb.Version(), 10))
	in.Del("machine")
	in.Set("url", "https://example.com/status")
	if a := owner.post("/console/monitors/"+svc+"/edit", in); a.code != http.StatusSeeOther {
		t.Fatalf("saving from an old form without the machine field: %d", a.code)
	}
	e.now = e.now.Add(time.Second)
	if m, ok := e.cdb.MonitorMachine(svc, "critical", e.now); !ok || m.Name != "edge-1" {
		t.Fatalf("old form removed a binding: %+v, %v", m, ok)
	}
	if _, ok := e.cdb.MonitorMachine(svc, "critical", originally); ok {
		t.Fatal("matched a delayed alert that began before the current definition")
	}
	if _, err = e.cdb.Import(ctx, actor, files, "restore the exported monitor", false); err != nil {
		t.Fatal(err)
	}
	e.cdb.Reload()
	e.now = e.now.Add(time.Second)
	if m, ok := e.cdb.MonitorMachine(svc, "critical", e.now); !ok || m.Name != "edge-1" {
		t.Fatalf("import or reload dropped the exact binding: %+v, %v", m, ok)
	}
	files["targets/all-duplicate.yaml"] = `[{"targets":["https://example.org/health"],"labels":{"service":"imported-designation","module":"http_2xx","severity":"critical","unrelated":"preserved"}}]`
	if _, err = e.cdb.Import(ctx, actor, files, "introduce an ambiguous service", false); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	if _, ok := e.cdb.MonitorMachine(svc, "critical", e.now); ok {
		t.Fatal("used a scoped target while another target carried the same service")
	}
}
