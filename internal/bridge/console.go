package bridge

import (
	"context"
	"fmt"

	"github.com/openlinkos/beacon/internal/store"
)

// What the console (package web) does beyond what the API already offers. Everything else the console
// changes goes through the same methods as the API and MCP, with the actor's Via "console".

// Revocable reports whether a token may be revoked from the console: one that reaches machines through
// the SSH channel (machines:ssh), or any agent's token. That is the pause switch for one agent:
// people stop what an agent does on machines at once, without stopping the others. The tokens of
// machines and services (probes, hosts, heartbeats, the rule engine, change senders, metrics readers)
// stay with the admin socket, where they were made: revoking one of them from a page could silence
// monitoring, and no alert may be lost.
func Revocable(t store.Token) bool { return t.Agent != "" || t.Has("machines:ssh") }

// RevokeToken revokes the token called name for the console's caller, ends its console sessions,
// records it in the audit log and echoes it to the chat, as every write is. Only Revocable tokens.
func (b *Bridge) RevokeToken(ctx context.Context, who Actor, name string) error {
	t, ok := b.token(name)
	if !ok {
		return fmt.Errorf("%w: no token %q", store.ErrNotFound, name)
	}
	if !Revocable(t) {
		return fmt.Errorf("%w: token %s does not reach machines; revoke it with beacon token revoke on the host", ErrBadInput, name)
	}
	if err := b.d.Store.RevokeToken(name); err != nil {
		return err
	}
	b.d.Store.DeleteConsoleSessions(name)
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "revoke_token", Target: name})
	b.echo(ctx, b.t("%s revoked token %s via %s", who.Name, name, b.viaName(who.Via)))
	return nil
}
