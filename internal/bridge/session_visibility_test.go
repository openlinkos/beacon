package bridge

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestAlertViewsHonourSessionMachineSelection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.b.d.Config.PublicURL = "https://beacon.example.com"
	w := outage("firing")
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	id := GroupID(w.GroupKey)
	actor := Actor{Name: "fixture operator", Via: "console"}
	if _, err := e.b.AddNote(ctx, id, actor, "Investigating the service."); err != nil {
		t.Fatal(err)
	}
	name := "selected-machine"
	earliest, err := e.st.RegisterMachine(store.Machine{Name: name, Address: "old.example.com:22", Added: e.now.Add(-time.Hour)}, nil, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	current, err := e.st.RegisterMachine(store.Machine{Name: name, Address: "selected.example.com:22", Added: e.now.Add(-3 * time.Minute)}, &earliest, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.st.RegisterMachine(store.Machine{Name: "other-machine", Address: "other.example.com:22", Added: e.now.Add(-time.Hour)}, nil, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	var allowed store.SessionRecord
	var relatedIDs []string
	for i, machine := range []store.Machine{current, earliest, other} {
		started := e.now.Add(-time.Duration(i+1) * time.Minute)
		r := store.SessionRecord{ID: started.Format("20060102-150405") + "-abcdef-1", Agent: actor.Name, Machine: machine.Name,
			MachineRegistration: machine.Registration(), Kind: "exec", Command: "fixture command", Started: started, Ended: started.Add(time.Second)}
		if err := e.st.PutSession(r); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			allowed = r
		}
		relatedIDs = append(relatedIDs, r.ID)
	}
	for _, detail := range []bool{false, true} {
		p := store.Principal{Scopes: []string{"status:read"}, Machines: []string{name}}
		if detail {
			p.Scopes = append(p.Scopes, "sessions:read")
		}
		ev, err := e.b.EvidenceFor(ctx, p, id, "")
		if err != nil || len(ev.Done.Sessions) != 1 || ev.Done.Sessions[0].ID != allowed.ID {
			t.Fatalf("evidence admitted another enrolment or lost the allowed session: %d rows, %v", len(ev.Done.Sessions), err)
		}
		row := ev.Done.Sessions[0]
		if detail {
			if row.Command != allowed.Command || row.Page != "https://beacon.example.com"+link.SignInPath(link.SessionPath(allowed.ID)) {
				t.Fatal("authorised evidence omitted the command or account-bound replay path")
			}
		} else if row.Command != "" || row.Page != "" {
			t.Fatal("metadata included private session detail")
		}
		tl, err := e.b.TimelineFor(ctx, p, id, "")
		if err != nil {
			t.Fatal(err)
		}
		var sessions int
		for _, event := range tl.Events {
			if event.Kind == "session" {
				sessions++
				if event.ID != allowed.ID || event.Text != row.Command || event.URL != row.Page || strings.Contains(event.URL, "?s=") {
					t.Error("timeline did not follow the same session visibility as evidence")
				}
			}
		}
		if sessions != 1 {
			t.Errorf("expected one session in timeline, got %d", sessions)
		}
	}
	o := store.IncidentOutcome{ID: store.OutcomeID(e.now, id, "legacy"), Group: id, Started: e.now.Add(-time.Hour),
		Resolved: e.now, Sessions: relatedIDs}
	if err := e.st.EnsureIncidentOutcome(o); err != nil {
		t.Fatal(err)
	}
	p := store.Principal{Scopes: []string{"status:read", "sessions:read"}, Machines: []string{name}}
	result, err := e.b.IncidentOutcomeFor(p, o.ID)
	if err != nil || len(result.Outcome.Sessions) != 1 || result.Outcome.Sessions[0] != allowed.ID {
		t.Fatalf("archived incident exposed unrelated sessions: %d rows, %v", len(result.Outcome.Sessions), err)
	}
	page, err := e.b.IncidentOutcomesFor(p, OutcomeQuery{Group: id, Limit: 1})
	if err != nil || len(page.Outcomes) != 1 || len(page.Outcomes[0].Sessions) != 1 || page.Outcomes[0].Sessions[0] != allowed.ID {
		t.Fatalf("archived incident list ignored the session scope: %v", err)
	}
}

func TestSessionAuditProjectionKeepsRecordedEvents(t *testing.T) {
	e := newEnv(t)
	for _, entry := range []store.AuditEntry{
		{Action: "ssh_subsystem", Target: "edge-1", Detail: "fixture subsystem"},
		{Action: "ssh_exec_refused", Target: "edge-1", Detail: "fixture refused command"},
		{Action: "ssh_exec", Target: "edge-1", Detail: "fixture command"},
		{Action: "ssh_key_add", Target: "alice", Detail: "test fingerprint"},
	} {
		if err := e.st.Audit(entry); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []store.Principal{
		{Scopes: []string{"status:read"}},
		{Scopes: []string{"status:read", "sessions:read"}, Machines: []string{"edge-1"}},
	} {
		entries := e.st.AuditLog(10)
		if len(entries) != 4 {
			t.Fatalf("audit fixture has %d entries", len(entries))
		}
		e.b.FilterSessionAudit(p, entries)
		if entries[0].Detail != "test fingerprint" {
			t.Fatal("audit projection should keep other activity")
		}
		for _, entry := range entries[1:] {
			if entry.Detail != "" || entry.Target != "edge-1" {
				t.Fatalf("audit projection should omit session detail: %+v", entry)
			}
		}
	}
	entries := e.st.AuditLog(10)
	e.b.FilterSessionAudit(store.Principal{Scopes: []string{"sessions:read"}}, entries)
	if entries[1].Detail != "fixture command" || entries[3].Detail != "fixture subsystem" {
		t.Fatal("projecting a credential's view changed underlying audit data or hid it from an unscoped reader")
	}
}

func TestSessionShareAlsoNeedsTheReadPermission(t *testing.T) {
	e := newEnv(t)
	signer, err := link.NewRevocableSigner(bytes.Repeat([]byte{1}, link.KeySize), time.Hour, e.b.now, e.st.PageLinkGeneration)
	if err != nil {
		t.Fatal(err)
	}
	e.b.d.Pages = &link.Pages{Signer: signer, PublicURL: "https://beacon.example.com"}
	m, err := e.st.RegisterMachine(store.Machine{Name: "selected-machine", Address: "machine.example.com:22", Added: e.now.Add(-time.Hour)}, nil, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	start := e.now.Add(-time.Minute)
	id := start.Format("20060102-150405") + "-abcdef-1"
	if err := e.st.PutSession(store.SessionRecord{ID: id, Machine: m.Name, MachineRegistration: m.Registration(), Started: start}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := e.st.CreateToken("admin", []string{"accounts:write", "sessions:read"}, time.Hour, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UpdateToken("admin", func(t *store.Token) error {
		t.Machines = []string{"other-machine"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	actor := func() Actor {
		t.Helper()
		p, ok := e.st.Authenticate(secret)
		if !ok {
			t.Fatal("test principal was refused")
		}
		return ActorOf(p, "api")
	}
	if _, err := e.b.CreateShareLink(context.Background(), actor(), "session", id); !errors.Is(err, ErrShareNotFound) {
		t.Fatal("an administrative token shared a session outside its machine selection")
	}
	if _, err := e.st.UpdateToken("admin", func(t *store.Token) error {
		t.Machines = []string{m.Name}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if shared, err := e.b.CreateShareLink(context.Background(), actor(), "session", id); err != nil || shared.URL == "" {
		t.Fatalf("an administrative token could not share the selected session: %v", err)
	}
}
