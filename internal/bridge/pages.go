package bridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/store"
)

// The pages people open from the chat (internal/web): an alert's page, with its timeline and the
// sessions on its machines, and each session's replay. They are reached by signed links that the bridge
// builds from public_url, so that a card, a chat message or an API answer can carry one. Without a
// public URL or a signer there are no links, and everything that would carry one leaves it out.

// PageURL is the signed link to the page at path, absolute under public_url; "" without one.
func (b *Bridge) PageURL(path string) string { return b.d.Pages.URL(path) }

// AlertPage is the signed link to an alert group's page.
func (b *Bridge) AlertPage(id string) string { return b.d.Pages.Alert(id) }

func (b *Bridge) incidentPage(g *store.Group) string { return b.d.Pages.Incident(g.ID, g.Episode) }

// SessionPage is the signed link to a session's page, with its replay.
func (b *Bridge) SessionPage(id string) string { return b.d.Pages.Session(id) }

type cardSessions struct {
	count, machines, commands int
	latest                    time.Time
}

// sessionSummary uses the same association and evidence window as the incident timeline.
func (b *Bridge) sessionSummary(g *store.Group) cardSessions {
	var summary cardSessions
	seen := map[string]bool{}
	for _, s := range b.relatedSessionRecords(g, b.window(g, defaultBefore), b.machines(g), nil) {
		if s.Ended.IsZero() {
			continue
		}
		summary.count++
		seen[s.Machine] = true
		if s.Ended.After(summary.latest) {
			summary.latest = s.Ended
		}
		switch s.Kind {
		case "shell":
			summary.commands += s.Commands
		case "exec", "file":
			summary.commands++
		}
	}
	summary.machines = len(seen)
	return summary
}

func (s cardSessions) fingerprint() string {
	if s.count == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d/%d", s.count, s.machines, s.commands, s.latest.UnixNano())
}

func (b *Bridge) sessionsBlock(s cardSessions) map[string]any {
	if s.count == 0 {
		return nil
	}
	return feishu.Markdown(strings.Join([]string{
		b.t("**Finished SSH sessions**"),
		b.t("%d sessions on %d machines, %d commands", s.count, s.machines, s.commands),
		b.t("Latest session ended at %s", b.clock(s.latest)),
	}, "\n"))
}

// agentBlock says which hooks were woken and how that went.
func (b *Bridge) agentBlock(g *store.Group) map[string]any {
	lines := b.agentLines(g)
	if len(lines) == 0 {
		return nil
	}
	return feishu.Markdown(b.t("**Agents**") + "\n" + strings.Join(lines, "\n"))
}
