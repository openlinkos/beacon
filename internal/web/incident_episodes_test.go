package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

func TestStartedEpisodesConsoleShowsAnOpenAlertAlongsideItsEvidence(t *testing.T) {
	e := newEnv(t, i18n.EN)
	viewer := e.invite(e.owner(), "episode-reviewer", "viewer")
	g, _ := e.st.Group(e.id)
	g.Agents = []store.AgentCall{{Event: "firing", Hook: "test-hook", Status: "ok", Run: &store.AgentRun{Status: "not_started"}}}
	if err := e.st.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	path := consolePath("/incident-episodes")
	history := viewer.get(consolePath("/incident-outcomes"))
	if history.code != http.StatusOK || !strings.Contains(history.body, path) {
		t.Fatalf("history link unavailable: %d", history.code)
	}
	page := viewer.get(path)
	if page.code != http.StatusOK {
		t.Fatalf("episode history unavailable: %d", page.code)
	}
	for _, part := range []string{"Started episodes", "Still open", "Accepted by hook", "Did not start", "No human acknowledgement recorded",
		"Unknown origin", link.IncidentPath(g.ID, g.Episode), "Started before (UTC)"} {
		if !strings.Contains(page.body, part) {
			t.Errorf("episode history lacks %q", part)
		}
	}
}
