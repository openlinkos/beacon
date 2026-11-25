package bridge

import "context"

// IncidentPage reads an alert page from one stored group snapshot. A recurring group's replacement
// cannot mix the previous episode's evidence or timeline with its own actions.
func (b *Bridge) IncidentPage(ctx context.Context, id, episode string) (GroupView, Evidence, Timeline, error) {
	g, ok := b.d.Store.Group(id)
	if !ok || (episode != "" && g.Episode != episode) {
		return GroupView{}, Evidence{}, Timeline{}, ErrNotFound
	}
	w := b.window(&g, defaultBefore)
	return b.view(&g), b.evidence(ctx, &g, w), b.timeline(ctx, &g, w), nil
}
