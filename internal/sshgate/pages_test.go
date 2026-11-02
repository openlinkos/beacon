package sshgate

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// TestReplayLinks checks that the chat's echoes and the session answers carry the signed link to each
// session's page once the channel has a public URL to build them from.
func TestReplayLinks(t *testing.T) {
	e := setup(t)
	zero := 0
	rec := store.SessionRecord{ID: "20261102-100000-abcdef-1", Agent: "claude-code", Via: "ssh", Machine: "box", Kind: "exec", Command: "uptime",
		Started: time.Now().Add(-time.Minute), Ended: time.Now(), Exit: &zero}
	if err := e.st.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	e.g.echo.add(rec)
	e.g.echo.flush(context.Background(), true)
	if got := e.bot.all(); !strings.Contains(got, "ran `uptime` (exit 0)") || strings.Contains(got, "Replay") {
		t.Errorf("without a public URL the echo has no link: %q", got)
	}
	ss, err := e.g.Sessions(SessionFilter{})
	if err != nil || len(ss) != 1 || ss[0].Page != "" {
		t.Errorf("sessions without a public URL: %+v %v", ss, err)
	}

	s, err := link.NewSigner(bytes.Repeat([]byte{5}, link.KeySize), time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.g.d.Pages = &link.Pages{Signer: s, PublicURL: "https://beacon.example.com"}
	want := "https://beacon.example.com/sessions/" + rec.ID + "?s="
	e.g.echo.add(rec)
	e.g.echo.flush(context.Background(), true)
	if got := e.bot.all(); !strings.Contains(got, "Replay: "+want) {
		t.Errorf("the echo lacks the replay link: %q", got)
	}
	ss, err = e.g.Sessions(SessionFilter{Machine: "box"})
	if err != nil || len(ss) != 1 || !strings.HasPrefix(ss[0].Page, want) || ss[0].ID != rec.ID {
		t.Errorf("sessions: %+v %v", ss, err)
	}
	tr, err := e.g.Session(rec.ID)
	if err != nil || !strings.HasPrefix(tr.Page, want) || tr.Command != "uptime" {
		t.Errorf("session: %+v %v", tr, err)
	}
	if _, err := e.g.Session("nope"); err == nil {
		t.Error("an unknown session answers")
	}
}
