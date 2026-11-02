package bridge

import (
	"strings"

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

// SessionPage is the signed link to a session's page, with its replay.
func (b *Bridge) SessionPage(id string) string { return b.d.Pages.Session(id) }

// sessionCounts counts the sessions in the group's window on its machines or by the agents that acted on
// it: how many, on how many machines, and how many commands they ran (each command run through exec or a
// file tool counts one; a shell counts the commands at its prompt).
func (b *Bridge) sessionCounts(g *store.Group) (sessions, machines, commands int) {
	seen := map[string]bool{}
	for _, s := range b.sessions(g, b.window(g, defaultBefore), b.machines(g)) {
		sessions++
		seen[s.Machine] = true
		switch s.Kind {
		case "shell":
			commands += s.Commands
		case "exec", "file":
			commands++
		}
	}
	return sessions, len(seen), commands
}

// agentBlock is the card's block about agents: which hooks were woken and how that went, and one line on
// the sessions on the alert's machines so far, with the link to the alert's page where every session can
// be replayed. Nil when there is nothing to say, and then left out of the card.
func (b *Bridge) agentBlock(g *store.Group) map[string]any {
	lines := b.agentLines(g)
	if n, m, k := b.sessionCounts(g); n > 0 {
		line := b.t("%d sessions on %d machines, %d commands", n, m, k)
		if u := b.AlertPage(g.ID); u != "" {
			line += " [" + b.t("Timeline") + "](" + u + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	return feishu.Markdown(b.t("**Agents**") + "\n" + strings.Join(lines, "\n"))
}
