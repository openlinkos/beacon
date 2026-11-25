package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestIncidentHistoryIsPrivateAndRevisitable(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	e.invite(owner, "archive-reader", "viewer")
	e.now = e.now.Add(time.Minute)
	if _, err := e.b.AddNote(context.Background(), e.id, bridge.ActorOf(store.Principal{User: "alice", Agent: "duty"}, "mcp"), "first episode notes"); err != nil {
		t.Fatal(err)
	}
	resolve := alertmanager.Webhook{GroupKey: groupKey, Status: "resolved", CommonLabels: map[string]string{"service": "git-example-com", "severity": "critical", "host": "edge-1"},
		CommonAnnotations: map[string]string{"summary": "git.example.com is down " + hostile}}
	if err := e.b.HandleWebhook(context.Background(), resolve); err != nil {
		t.Fatal(err)
	}
	retained, err := e.b.IncidentOutcomes(e.id, "", 1)
	if err != nil || len(retained.Outcomes) != 1 {
		t.Fatalf("fixture outcome: %+v %v", retained, err)
	}
	path := outcomePath(retained.Outcomes[0].ID)
	e.now = e.now.Add(8 * 24 * time.Hour)
	e.b.Tick(context.Background())

	// A later action on the recurring group has the same target but belongs to another time window.
	if err := e.st.Audit(store.AuditEntry{Time: e.now.Add(time.Minute), Actor: "other", Via: "api", Action: "add_note",
		Target: e.id, Detail: "later episode notes"}); err != nil {
		t.Fatal(err)
	}
	if a := e.browser().get(path); a.code != http.StatusSeeOther || !strings.Contains(a.header.Get("Location"), "sign-in") {
		t.Fatalf("archive secret needs sign-in: %d", a.code)
	}
	if code, _, _ := e.get(e.link(link.AlertPath(e.id)), ""); code != http.StatusNotFound {
		t.Fatalf("a signed rich alert link did not expire with the rich record: %d", code)
	}
	b := e.browser()
	if a := b.signInAs("archive-reader", password); a.code != http.StatusSeeOther {
		t.Fatalf("viewer sign-in: %d", a.code)
	}
	alerts := b.get(consolePath("/alerts"))
	if alerts.code != http.StatusOK || !strings.Contains(alerts.body, "Incident history") {
		t.Errorf("alerts misses incident history: %d", alerts.code)
	}
	arch := b.get(path)
	if arch.code != http.StatusOK || !strings.Contains(arch.body, "Alert resolved") || !strings.Contains(arch.body, "alice / duty") ||
		strings.Contains(arch.body, "first episode notes") || strings.Contains(arch.body, hostile) || strings.Contains(arch.body, "Open alert details") {
		t.Fatalf("protected compact outcome: %d", arch.code)
	}
	if !strings.Contains(arch.body, link.SessionPath(sessionID)) {
		t.Fatal("the viewer cannot reach the retained session from the archived incident")
	}
	if a := e.signedIn("read-only", "status:read").get(path); a.code != http.StatusOK || strings.Contains(a.body, link.SessionPath(sessionID)) {
		t.Fatalf("status:read alone should not show a recording link: %d", a.code)
	}
	filtered := b.get(consolePath("/audit") + "?outcome=" + url.QueryEscape(retained.Outcomes[0].ID))
	if filtered.code != http.StatusOK || !strings.Contains(filtered.body, "first episode notes") || strings.Contains(filtered.body, "later episode notes") {
		t.Fatalf("filtered audit: %d", filtered.code)
	}
	if a := b.get(link.AlertPath(e.id)); a.code != http.StatusSeeOther || a.header.Get("Location") != consolePath("/incident-outcomes")+"?group="+e.id {
		t.Fatalf("archived alert path: %d %q", a.code, a.header.Get("Location"))
	}
}

func TestNotificationIncidentStaysPinnedWhenGroupFiresAgain(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, i18n.EN)
	shift := e.invite(e.owner(), "incident-shift", "oncall")
	first, err := e.b.Group(e.id)
	if err != nil || !link.ValidEpisode(first.Episode) {
		t.Fatalf("first incident: %+v, %v", first, err)
	}
	firstPath := link.IncidentPath(e.id, first.Episode)
	firstURL, err := url.Parse(first.Page)
	if err != nil || firstURL.Path != firstPath {
		t.Fatalf("notification path: %q, %v", first.Page, err)
	}
	unsigned := e.browser().get(firstPath)
	if unsigned.code != http.StatusNotFound {
		t.Fatalf("unsigned link response: %d", unsigned.code)
	}
	if a := e.browser().get(firstURL.RequestURI()); a.code != http.StatusOK || !strings.Contains(a.body, link.SignInPath(firstPath)) {
		t.Fatalf("signed incident and sign-in destination: %d", a.code)
	}
	old := shift.get(firstPath)
	base := link.AlertPath(e.id)
	note := incidentForm(t, old, url.Values{"text": {"first incident handover"}})
	if a := shift.post(base+"/notes", note); a.header.Get("Location") != firstPath+"?done=note" {
		t.Fatalf("keeping incident after submission: %s", a.header.Get("Location"))
	}

	e.now = e.now.Add(time.Minute)
	received := alertmanager.Webhook{GroupKey: groupKey, Status: "resolved",
		CommonLabels:      map[string]string{"service": "git-example-com", "severity": "critical"},
		CommonAnnotations: map[string]string{"summary": "First incident recovered"}}
	if err := e.b.HandleWebhook(ctx, received); err != nil {
		t.Fatal(err)
	}
	archived := outcomePath(store.OutcomeID(e.now, e.id, first.Episode))
	e.now = e.now.Add(time.Minute)
	received.Status = "firing"
	received.CommonAnnotations["summary"] = "Second incident firing"
	if err := e.b.HandleWebhook(ctx, received); err != nil {
		t.Fatal(err)
	}
	current, err := e.b.Group(e.id)
	if err != nil || current.Episode == first.Episode {
		t.Fatalf("second incident: %+v, %v", current, err)
	}
	currentPath := link.IncidentPath(e.id, current.Episode)

	for _, check := range []struct {
		form   string
		fields url.Values
		error  string
	}{
		{"ack", ackForm(t, old, "wrong episode"), "ack_changed"},
		{"silence", incidentForm(t, old, url.Values{"duration": {"1h"}, "comment": {"wrong episode"}}), "incident_changed"},
		{"notes", incidentForm(t, old, url.Values{"text": {"wrong episode"}}), "incident_changed"},
	} {
		a := shift.post(base+"/"+check.form, check.fields)
		if a.code != http.StatusSeeOther || a.header.Get("Location") != firstPath+"?error="+check.error {
			t.Fatalf("stale %s: %d, %s", check.form, a.code, a.header.Get("Location"))
		}
		a = shift.get(a.header.Get("Location"))
		if a.header.Get("Location") != archived+"?error="+check.error {
			t.Fatalf("stale form should return to its own history: %s", a.header.Get("Location"))
		}
	}
	g, _ := e.st.Group(e.id)
	if g.Ack != nil || g.Silence != nil || len(g.Notes) != 0 || e.am.made != 0 {
		t.Fatalf("stale actions changed the new instance: %+v", g)
	}
	if a := e.browser().get(currentPath + "?" + firstURL.RawQuery); a.code != http.StatusNotFound {
		t.Fatalf("the first episode's signature opened a different episode: %d", a.code)
	}
	if a := e.browser().get(firstURL.RequestURI()); a.header.Get("Location") != link.SignInPath(firstPath) {
		t.Fatalf("signed archive should request an account before proceeding: %d, %s", a.code, a.header.Get("Location"))
	}
	if a := shift.get(firstPath); a.header.Get("Location") != archived {
		t.Fatalf("old link opened something other than its own resolved incident: %d, %s", a.code, a.header.Get("Location"))
	}
	if a := shift.get(archived + "?error=incident_changed"); a.code != http.StatusOK ||
		!strings.Contains(a.body, "First incident recovered") || !strings.Contains(a.body, "Review the alert before taking action.") ||
		strings.Contains(a.body, "Second incident firing") || strings.Contains(a.body, "Open alert details") {
		t.Fatalf("archive did not separate the episodes: %d", a.code)
	}
	second := shift.get(currentPath)
	if second.code != http.StatusOK || !strings.Contains(second.body, "Second incident firing") || strings.Contains(second.body, "first incident handover") {
		t.Fatalf("second incident page: %d", second.code)
	}
	if a := shift.post(base+"/ack", ackForm(t, second, "current response")); a.header.Get("Location") != currentPath+"?done=ack" {
		t.Fatalf("handover on the current incident: %q", a.header.Get("Location"))
	}
}
