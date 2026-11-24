package bridge

import (
	"context"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

const sessionCardsPerTick = 32

// refreshSessionCards reconciles the Feishu cards with the stored sessions on the leader's tick.
// A persisted summary survives a restart or takeover, and several closes can share one patch.
// This runs after escalation, rotates through the groups and attempts at most one patch per tick.
func (b *Bridge) refreshSessionCards(ctx context.Context, groups []store.Group, now time.Time) {
	if b.d.Feishu == nil || len(groups) == 0 {
		return
	}
	start := b.sessionCardCursor % len(groups)
	for i := range min(len(groups), sessionCardsPerTick) {
		if ctx.Err() != nil {
			return
		}
		index := (start + i) % len(groups)
		b.sessionCardCursor = (index + 1) % len(groups)
		g := groups[index]
		if (!g.Firing() && now.Sub(g.ResolvedAt) > keepResolved) ||
			now.Sub(g.CardAt) < patchEvery || now.Sub(g.CardSessionAttemptAt) < patchEvery || !b.patchAllowed(&g, false) {
			continue
		}
		sessions := b.sessionSummary(&g)
		if sessions.fingerprint() == g.CardSessionHash {
			continue
		}
		patchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		g.CardSessionAttemptAt = now
		b.patchWithSessions(patchCtx, &g, sessions)
		cancel()
		if err := b.d.Store.PutGroup(g); err != nil {
			b.d.Log.Error("save session card state", "group", g.ID, "err", err)
		}
		return
	}
}
