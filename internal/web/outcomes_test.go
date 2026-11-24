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
