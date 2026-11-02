package bridge

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// withPages gives the env a public URL and a signer, as a deployment with pages has.
func withPages(t *testing.T, e *env) *link.Signer {
	t.Helper()
	s, err := link.NewSigner(bytes.Repeat([]byte{3}, link.KeySize), 30*24*time.Hour, func() time.Time { return e.now })
	if err != nil {
		t.Fatal(err)
	}
	e.b.d.Config.PublicURL = "https://beacon.example.com"
	e.b.d.Pages = &link.Pages{Signer: s, PublicURL: e.b.d.Config.PublicURL}
	return s
}

func verifies(t *testing.T, s *link.Signer, u, path string) {
	t.Helper()
	p, err := url.Parse(u)
	if err != nil || p.Host != "beacon.example.com" || p.Path != path {
		t.Fatalf("link %q: %v", u, err)
	}
	if _, ok := s.Verify(http.MethodGet, p.Path, p.Query().Get(link.Param)); !ok {
		t.Errorf("link %q does not verify", u)
	}
}

func TestPageLinksAndCardBlock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if e.b.AlertPage("abc") != "" || e.b.PageURL("/alerts/abc") != "" {
		t.Error("links without a public URL")
	}
	s := withPages(t, e)
	w := hook("firing", "critical", "f1")
	w.CommonLabels["host"] = "edge-1"
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	id := GroupID(w.GroupKey)
	verifies(t, s, e.b.AlertPage(id), link.AlertPath(id))
	verifies(t, s, e.b.SessionPage("20261030-015900-abcdef-1"), link.SessionPath("20261030-015900-abcdef-1"))

	// sessions on the alert's machine in its window: a shell with two commands and one exec
	zero := 0
	for _, r := range []store.SessionRecord{
		{ID: "20261030-015900-abcdef-1", Agent: "claude-code", Via: "ssh", Machine: "edge-1", Kind: "shell", Started: e.now.Add(-time.Minute),
			Ended: e.now.Add(-30 * time.Second), Exit: &zero, Commands: 2, Recording: "2026/10/30/20261030-015900-abcdef-1"},
		{ID: "20261030-015930-abcdef-1", Agent: "claude-code", Via: "mcp", Machine: "edge-1", Kind: "exec", Command: "systemctl status relay",
			Started: e.now.Add(-30 * time.Second), Ended: e.now.Add(-29 * time.Second), Exit: &zero},
		{ID: "20261030-015940-abcdef-1", Agent: "codex", Via: "mcp", Machine: "edge-9", Kind: "exec", Command: "uptime", Started: e.now, Ended: e.now, Exit: &zero},
	} {
		if err := e.st.PutSession(r); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := e.st.Group(id)
	c := cardText(e.b.card(ctx, &g))
	if !strings.Contains(c, "1 sessions on 1 machines, 3 commands") && !strings.Contains(c, "2 sessions on 1 machines, 3 commands") {
		t.Errorf("the card lacks the sessions line: %s", c)
	}
	if !strings.Contains(c, "2 sessions on 1 machines, 3 commands") {
		t.Errorf("sessions line: %s", c)
	}
	if !strings.Contains(c, `**Agents**`) || !strings.Contains(c, `[Timeline](https://beacon.example.com/alerts/`+id+`?s=`) {
		t.Errorf("the agent block lacks its title or the page link: %s", c)
	}
	if !strings.Contains(c, `"default_url":"https://beacon.example.com/alerts/`+id+`?s=`) || !strings.Contains(c, `"content":"Timeline"`) {
		t.Errorf("the card lacks the Timeline button: %s", c)
	}

	// the API and MCP answers carry the links too
	view, err := e.b.Group(id)
	if err != nil || view.Page == "" {
		t.Fatalf("group view: %+v %v", view, err)
	}
	verifies(t, s, view.Page, link.AlertPath(id))
	ev, err := e.b.Evidence(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	verifies(t, s, ev.Links.Page, link.AlertPath(id))
	if len(ev.Done.Sessions) != 2 {
		t.Fatalf("sessions: %+v", ev.Done.Sessions)
	}
	for _, v := range ev.Done.Sessions {
		verifies(t, s, v.Page, link.SessionPath(v.ID))
	}
	if ev.Done.Sessions[0].Commands != 2 {
		t.Errorf("the shell's commands are not counted: %+v", ev.Done.Sessions[0])
	}
	tl, err := e.b.Timeline(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range tl.Events {
		if ev.Kind == "session" {
			verifies(t, s, ev.URL, link.SessionPath(ev.ID))
			n++
		}
	}
	if n != 2 {
		t.Errorf("session events: %d", n)
	}

	// without sessions or agents, the block is left out
	e.st.DeleteSessionsBefore(e.now.Add(time.Hour))
	g, _ = e.st.Group(id)
	if c := cardText(e.b.card(ctx, &g)); strings.Contains(c, "**Agents**") {
		t.Errorf("an empty agent block: %s", c)
	}
}
