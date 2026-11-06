package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

func amSilence(id, comment, service string, ends time.Time) alertmanager.Silence {
	var s alertmanager.Silence
	s.ID, s.Comment, s.CreatedBy, s.EndsAt = id, comment, "operator", ends
	s.StartsAt = ends.Add(-time.Hour)
	s.Matchers = []alertmanager.Matcher{{Name: "service", Value: service, IsEqual: true}}
	s.Status.State = "active"
	return s
}

func TestSilencesPage(t *testing.T) {
	e := newEnv(t, i18n.EN)
	ctx, who := context.Background(), bridge.Actor{Name: "alice", Via: "api"}
	soon, err := e.b.ScheduleMaintenance(ctx, who, bridge.MaintenanceInput{Services: []string{"git-example-com"}, StartsAt: e.now.Add(time.Hour),
		EndsAt: e.now.Add(2 * time.Hour), Title: "Database upgrade " + hostile})
	if err != nil {
		t.Fatal(err)
	}
	now, err := e.b.ScheduleMaintenance(ctx, who, bridge.MaintenanceInput{Services: []string{"api"}, EndsAt: e.now.Add(time.Hour), Title: "Relay move"})
	if err != nil {
		t.Fatal(err)
	}
	w := e.signedIn("operator", "status:read", "alerts:write", "maintenance:write")
	if a := w.post(link.AlertPath(e.id)+"/silence", url.Values{"duration": {"1h"}, "comment": {"deploying"}}); a.code != http.StatusSeeOther {
		t.Fatalf("silencing: %d", a.code)
	}
	// the windows made sil-1 and sil-2, the alert's page sil-3
	e.am.silences = []alertmanager.Silence{
		amSilence("sil-3", "deploying "+hostile, "git-example-com", e.now.Add(time.Hour)),
		amSilence("sil-2", bridge.MaintenancePrefix+" Relay move", "api", e.now.Add(time.Hour)),
	}

	page := w.get("/console/silences")
	checkPage(t, "/console/silences", page.body)
	for _, want := range []string{
		"Database upgrade &lt;script&gt;", "Scheduled", "Call off", "Relay move", "In progress", "End now",
		`action="/console/maintenance/` + soon.ID + `/end"`, `action="/console/maintenance/` + now.ID + `/end"`,
		`service=&#34;git-example-com&#34;`, "deploying &lt;script&gt;", "git.example.com is down &lt;script&gt;", "1 h left",
		`action="/console/silences/sil-3/expire"`, "Ends with its maintenance window", "4 in force or planned",
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page.body, "sil-2/expire") {
		t.Error("a maintenance window's silence is ended on its own")
	}
	// the sidebar counts the windows planned
	if !strings.Contains(page.body, `Silences and maintenance</span><span class="ol-nav-count">2</span>`) {
		t.Error("the sidebar does not count the windows")
	}

	// someone who may only read sees it all and no forms
	r := e.signedIn("reader", "status:read")
	if body := r.get("/console/silences").body; strings.Contains(body, "/expire\"") || strings.Contains(body, "/end\"") || !strings.Contains(body, "Relay move") {
		t.Error("a reader is offered the forms, or misses the windows")
	}
	if a := r.post("/console/silences/sil-3/expire", nil); a.code != http.StatusForbidden || len(e.am.expired) != 0 {
		t.Errorf("a reader ended a silence: %d %v", a.code, e.am.expired)
	}

	// the alert's page ends its silence, and comes back to it
	alert := w.get(link.AlertPath(e.id)).body
	if !strings.Contains(alert, `action="/console/silences/sil-3/expire"`) || !strings.Contains(alert, `name="back" value="`+link.AlertPath(e.id)+`"`) {
		t.Error("the alert's page does not offer to end its silence")
	}
	a := w.post("/console/silences/sil-3/expire", url.Values{"back": {link.AlertPath(e.id)}})
	if a.code != http.StatusSeeOther || a.header.Get("Location") != link.AlertPath(e.id)+"?done=unsilenced" || !slices.Equal(e.am.expired, []string{"sil-3"}) ||
		!audited(e.st, "operator", "expire_silence") {
		t.Errorf("ending the silence: %d %q %v", a.code, a.header.Get("Location"), e.am.expired)
	}
	if g, _ := e.st.Group(e.id); g.Silence != nil {
		t.Error("the group is still silenced")
	}
	// a page to come back to that is not the console's is not followed
	if a := w.post("/console/silences/sil-9/expire", url.Values{"back": {"https://example.com/"}}); a.header.Get("Location") != "/console/silences?done=unsilenced" {
		t.Errorf("back elsewhere: %q", a.header.Get("Location"))
	}

	// a window in progress ends; one to come is called off, once
	for _, c := range []struct{ id, want string }{
		{now.ID, "/console/silences?done=maintenance_ended"},
		{soon.ID, "/console/silences?done=called_off"},
		{soon.ID, "/console/silences?error=window_over"},
	} {
		if a := w.post("/console/maintenance/"+c.id+"/end", nil); a.header.Get("Location") != c.want {
			t.Errorf("ending %s: %d %q, want %q", c.id, a.code, a.header.Get("Location"), c.want)
		}
	}
	if m, _ := e.st.MaintenanceWindow(now.ID); m.Ended.IsZero() || m.Cancelled || !audited(e.st, "operator", "end_maintenance") {
		t.Errorf("the window in progress: %+v", m)
	}
	if m, _ := e.st.MaintenanceWindow(soon.ID); !m.Cancelled {
		t.Errorf("the window to come: %+v", m)
	}
	if a := w.post("/console/maintenance/m-0000/end", nil); a.code != http.StatusNotFound {
		t.Errorf("no such window: %d", a.code)
	}
	// ending a window needs maintenance:write, as the API's DELETE does
	s := e.signedIn("silencer", "status:read", "alerts:write")
	if a := s.post("/console/maintenance/"+now.ID+"/end", nil); a.code != http.StatusForbidden {
		t.Errorf("without maintenance:write: %d", a.code)
	}
	if body := w.get("/console/silences").body; !strings.Contains(body, "Completed") || !strings.Contains(body, "Called off") {
		t.Error("the ended windows are not listed as such")
	}
}

func TestOverviewAttention(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.signedIn("root", "status:read")
	page := b.get("/console").body
	for _, want := range []string{
		"3 things need attention", "Firing alerts: 1; Probe locations not reporting: 1; Machines not connected: 1.", "ol-alert-danger",
		`href="` + link.AlertPath(e.id) + `"`, "No probe results in the last 5 minutes", `href="/console/machines/edge-2"`, "Behind NAT, not connected",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
	// the critical alert first
	if i, j := strings.Index(page, `href="`+link.AlertPath(e.id)+`"`), strings.Index(page, `href="/console/machines/edge-2"`); i < 0 || j < i {
		t.Error("the alert is not first")
	}
	// someone took the alert on: it stays, after what nobody has
	if _, err := e.b.Ack(context.Background(), e.id, bridge.Actor{Name: "alice", Via: "api"}, ""); err != nil {
		t.Fatal(err)
	}
	if page := b.get("/console").body; !strings.Contains(page, "Acknowledged") {
		t.Error("the acknowledged alert is not marked")
	}

	h := e.srv.healthOf(nil, serviceHealth{Total: 3})
	if h.Tone != "success" || h.Title != "Everything is running" || h.Text != "Nothing is firing, and all 3 services answer at every location." {
		t.Errorf("all well: %+v", h)
	}
	if h := e.srv.healthOf(nil, serviceHealth{Total: 3, Problem: "vm down"}); h.Tone != "warning" || !strings.Contains(h.Text, "vm down") {
		t.Errorf("the probes unread: %+v", h)
	}
	one := []attentionItem{{Tone: "warning", kind: "service"}}
	if h := e.srv.healthOf(one, serviceHealth{}); h.Title != "One thing needs attention" || h.Text != "Services in trouble: 1." || h.Tone != "warning" {
		t.Errorf("one service: %+v", h)
	}
}
