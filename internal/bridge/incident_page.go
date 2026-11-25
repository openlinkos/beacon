package bridge

import (
	"context"

	"github.com/openlinkos/beacon/internal/store"
)

// IncidentPageFor reads the evidence and timeline from one group snapshot under the same session policy.
func (b *Bridge) IncidentPageFor(ctx context.Context, id, episode string, p store.Principal) (GroupView, Evidence, Timeline, error) {
	g, ok := b.d.Store.Group(id)
	if !ok || (episode != "" && g.Episode != episode) {
		return GroupView{}, Evidence{}, Timeline{}, ErrNotFound
	}
	w := b.window(&g, defaultBefore)
	access := b.d.Store.SessionAccess(p)
	return b.view(&g), b.evidence(ctx, &g, w, &access), b.timeline(ctx, &g, w, &access), nil
}
