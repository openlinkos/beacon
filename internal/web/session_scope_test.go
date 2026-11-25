package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestConsoleSessionMachineScope(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	reader := e.invite(owner, "shift-reader", "viewer")
	selected, _ := e.st.Machine("edge-1")
	outside, _ := e.st.Machine("edge-2")
	first, _ := e.st.Session(sessionID)
	first.MachineRegistration, first.Command = selected.Registration(), "selected-command-marker"
	other := first
	other.ID, other.Machine, other.MachineRegistration, other.Command = "20261102-095601-fedcba-1", outside.Name, outside.Registration(), "outside-command-marker"
	stale := first
	stale.ID, stale.MachineRegistration, stale.Command = "20261102-095602-fedcba-1", "old-enrolment", "prior-enrolment-marker"
	for _, rec := range []store.SessionRecord{first, other, stale} {
		if err := e.st.PutSession(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.Audit(store.AuditEntry{Actor: other.Agent, Via: "ssh", Action: "ssh_exec", Target: other.Machine, Detail: other.Command}); err != nil {
		t.Fatal(err)
	}
	const restrictedName = "limited-reviewer"
	scoped := e.token(restrictedName, "status:read", "sessions:read")
	selectMachine := func(machine string) {
		t.Helper()
		if _, err := e.st.UpdateToken(restrictedName, func(t *store.Token) error { t.Machines = []string{machine}; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	selectMachine(selected.Name)
	restricted := e.browser()
	if a := restricted.signIn(scoped, "/console/sessions"); a.code != http.StatusSeeOther {
		t.Fatalf("restricted legacy sign-in: %d", a.code)
	}
	list := "/console/sessions"
	alert := link.AlertPath(e.id)
	resolved := e.now.Add(-time.Minute)
	archived := store.IncidentOutcome{ID: store.OutcomeID(resolved, e.id, "legacy"), Group: e.id, Title: "Archived incident", Resolved: resolved,
		Started: e.now.Add(-time.Hour), Sessions: []string{first.ID, other.ID, stale.ID}}
	if err := e.st.EnsureIncidentOutcome(archived); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{list, consolePath(""), alert, machinePath(selected.Name), outcomePath(archived.ID)} {
		a := restricted.get(path)
		if a.code != http.StatusOK || !strings.Contains(a.body, link.SessionPath(first.ID)) {
			t.Fatalf("%s lost allowed session: %d", path, a.code)
		}
		for _, hidden := range []string{other.ID, stale.ID, other.Command, stale.Command} {
			if strings.Contains(a.body, hidden) {
				t.Errorf("%s included an unrelated enrolment's session %q", path, hidden)
			}
		}
	}
	if a := restricted.get(machinePath(outside.Name)); a.code == http.StatusOK && strings.Contains(a.body, other.ID) {
		t.Fatal("an out-of-scope machine page exposed a session")
	}
	if a := restricted.get(consolePath("/audit")); a.code != http.StatusOK || strings.Contains(a.body, other.Command) {
		t.Fatalf("scoped audit exposed a legacy SSH command: %d", a.code)
	}
	if a := restricted.get(list); !strings.Contains(a.body, "Sessions shown here are limited") {
		t.Fatal("machine selection is missing from the list explanation")
	}
	if a := reader.get(alert); a.code != http.StatusOK || !strings.Contains(a.body, other.Command) || !strings.Contains(a.body, first.Command) {
		t.Fatalf("the authorised account lost session context in the alert: %d", a.code)
	}
	if a := e.browser().get(e.link(alert)); a.code != http.StatusOK || strings.Contains(a.body, first.ID) || strings.Contains(a.body, other.ID) ||
		!strings.Contains(a.body, link.SignInPath(alert)) || !strings.Contains(a.body, "SSH session history") {
		t.Fatalf("an anonymous alert share exposed session records or lost the sign-in path: %d", a.code)
	}
	for _, p := range []string{link.SessionPath(other.ID), link.RecordingPath(other.ID), link.SessionPath(stale.ID)} {
		shared := e.link(p)
		for _, target := range []string{p, shared} {
			if a := restricted.get(target); a.code != http.StatusNotFound {
				t.Errorf("restricted console access widened via %s: %d", p, a.code)
			}
			if code, _, _ := e.get(target, scoped); code != http.StatusNotFound {
				t.Errorf("restricted bearer access widened via %s: %d", p, code)
			}
		}
		if a := e.browser().get(shared); a.code != http.StatusOK {
			t.Errorf("explicit anonymous share stopped working for %s: %d", p, a.code)
		}
	}
	if a := restricted.get(link.RecordingPath(first.ID)); a.code != http.StatusOK || a.header.Get("Content-Type") != "application/x-asciicast" {
		t.Fatalf("allowed recording: %d", a.code)
	}
	metadata := e.signedIn("metadata-legacy", "status:read")
	if a := metadata.get(list); a.code != http.StatusOK || !strings.Contains(a.body, "Command text and recordings require permission") ||
		strings.Contains(a.body, first.Command) || strings.Contains(a.body, link.SessionPath(first.ID)) {
		t.Fatalf("metadata-only sign-in received session detail: %d", a.code)
	}
	if a := metadata.get(list + "?q=" + url.QueryEscape(first.Command)); a.code != http.StatusOK || !strings.Contains(a.body, "Nothing matches.") {
		t.Fatalf("command search exposed record details: %d", a.code)
	}
	if a := metadata.get(e.link(link.RecordingPath(first.ID))); a.code != http.StatusNotFound {
		t.Fatalf("a share elevated a signed-in metadata-only reader: %d", a.code)
	}
	selectMachine("unavailable-machine")
	if a := restricted.get(list); a.code != http.StatusOK || strings.Contains(a.body, first.ID) || !strings.Contains(a.body, "No sessions are available for your machine access.") {
		t.Fatalf("a current sign-in did not follow the changed machine access: %d", a.code)
	}
}
