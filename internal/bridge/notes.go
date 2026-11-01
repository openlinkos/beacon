package bridge

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/store"
)

// Notes are what people and agents leave on an alert group: what they found, what they did. The card
// shows the latest ones, so everyone on call reads what an agent is up to.

const (
	maxNote   = 2000 // characters in a note
	keepNotes = 100  // notes kept on a group; the audit log has every one
	cardNotes = 3    // notes the card shows
)

// AddNote leaves a note on an alert group, firing or resolved. The card shows it; it is audited and
// echoed to the alert chat. A note does not acknowledge the group.
func (b *Bridge) AddNote(ctx context.Context, id string, who Actor, text string) (GroupView, error) {
	text = clean(text, maxNote+1, true)
	switch {
	case text == "":
		return GroupView{}, fmt.Errorf("%w: the note is empty; say what was found or done", ErrBadInput)
	case utf8.RuneCountInString(text) > maxNote:
		return GroupView{}, fmt.Errorf("%w: a note has at most %d characters", ErrBadInput, maxNote)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	g.Notes = append(g.Notes, store.Note{By: who.Name, Via: who.Via, Text: text, At: b.now()})
	if len(g.Notes) > keepNotes {
		g.Notes = g.Notes[len(g.Notes)-keepNotes:]
	}
	if b.patchAllowed(&g, false) {
		b.patch(ctx, &g)
	}
	if err := b.d.Store.PutGroup(g); err != nil {
		return GroupView{}, err
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "add_note", Target: g.ID, Detail: clean(text, 500, false)})
	b.echo(ctx, b.t("%s left a note on \"%s\" via %s: %s", who.Name, chatSafe(b.title(&g)), b.viaName(who.Via), chatSafe(text)))
	return b.view(&g), nil
}

// notesBlock is the card's notes: the latest few, after how many came before; nil without notes.
func (b *Bridge) notesBlock(g *store.Group) map[string]any {
	if len(g.Notes) == 0 {
		return nil
	}
	lines := []string{b.t("**Notes**")}
	first := max(0, len(g.Notes)-cardNotes)
	if first > 0 {
		lines = append(lines, "<font color='grey'>"+b.t("%d earlier notes", first)+"</font>")
	}
	for _, n := range g.Notes[first:] {
		lines = append(lines, b.t("%s (%s, %s): %s", mdEscape(n.By), b.viaName(n.Via), b.clock(n.At), mdEscape(clean(n.Text, maxLine, false))))
	}
	return feishu.Markdown(strings.Join(lines, "\n"))
}

// agentLines say on the card which agents were woken for the group, and how that went.
func (b *Bridge) agentLines(g *store.Group) []string {
	var out []string
	for _, c := range g.Agents {
		if c.Event != "firing" {
			continue
		}
		switch c.Status {
		case "ok":
			out = append(out, b.t("Agent %s woken at %s", c.Hook, b.clock(c.Done)))
		case "failed":
			out = append(out, b.t("Could not wake agent %s", c.Hook))
		default:
			out = append(out, b.t("Waking agent %s", c.Hook))
		}
	}
	return out
}

// noteDigest is what of the notes and agent calls the card shows, for cardHash.
func noteDigest(g *store.Group) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "notes:%d", len(g.Notes))
	for _, c := range g.Agents {
		sb.WriteString("|" + c.Hook + ":" + c.Event + ":" + c.Status)
	}
	return sb.String()
}
