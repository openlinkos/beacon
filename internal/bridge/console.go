package bridge

import (
	"context"
	"fmt"

	"github.com/openlinkos/beacon/internal/store"
)

// What the console (package web) does beyond what the API already offers. Everything else the console
// changes goes through the same methods as the API and MCP, with the actor's Via "console".

// Revocable reports whether a token may be deleted from the console, the API and MCP: one that reaches
// machines through the SSH channel (machines:ssh), or any agent's token. Deleting it stops one agent's
// credential at once, without stopping the others; the agent itself stays. The tokens of
// machines and services (probes, hosts, heartbeats, the rule engine, change senders, metrics readers)
// stay with the admin socket, where they were made: revoking one of them from a page could silence
// monitoring, and no alert may be lost.
func Revocable(t store.Token) bool { return t.Agent != "" || t.Has("machines:ssh") }

// RevokeToken deletes the token called name for who (the console, the API and MCP), ends its console
// sessions and closes what it has open on machines, records it in the audit log and echoes it to the
// chat, as every write is. Only Revocable tokens, and only by someone who may (mayRevoke).
func (b *Bridge) RevokeToken(ctx context.Context, who Actor, name string) error {
	if err := b.mayRevokeAny(who); err != nil {
		return err
	}
	t, ok := b.token(name)
	if !ok {
		return fmt.Errorf("%w: no token %q", store.ErrNotFound, name)
	}
	if !Revocable(t) {
		return fmt.Errorf("%w: token %s does not reach machines; revoke it with beacon token revoke on the host", ErrBadInput, name)
	}
	if err := b.mayRevoke(who, t); err != nil {
		return err
	}
	return b.revokeToken(ctx, who, name)
}

// mayRevokeAny reports whether who may delete tokens at all: the command line; anyone else with
// machines:write and access to all machines. A credential limited to some machines changes nobody's
// access but its own. It is checked before the token is looked up.
func (b *Bridge) mayRevokeAny(who Actor) error {
	if cliActor(who) {
		return nil
	}
	p, ok := b.caller(who)
	switch {
	case !ok || !p.Has("machines:write"):
		return refusedBeforeLookup{fmt.Errorf("%w: deleting a token requires the machines:write scope", ErrForbidden)}
	case !p.IsAll():
		return refusedBeforeLookup{fmt.Errorf("%w: deleting a token requires access to all machines; this credential reaches only the machines it names", ErrForbidden)}
	}
	return nil
}

// mayRevoke reports whether who may delete t (mayRevokeAny holds already). An agent's token: the agent
// itself, a person (one with machines:write: an operator or an owner, who may stop any agent's
// credential at once), or an admin who manages agents; never another agent or another token. A token
// of its own: only one within the caller, every scope it holds held by the caller and its machine
// access within the caller's, so that nobody deletes a credential stronger than their own.
func (b *Bridge) mayRevoke(who Actor, t store.Token) error {
	if cliActor(who) {
		return nil
	}
	p, ok := b.caller(who)
	if !ok {
		return ErrForbidden
	}
	if t.Agent != "" {
		if p.Agent == t.Agent || p.Person() {
			return nil
		}
		a, ok := b.d.Store.Agent(t.Agent)
		if !ok || !b.mayManage(who, a.Owner) {
			return fmt.Errorf("%w: token %s is agent %s's; a person, or the agent itself, deletes it", ErrForbidden, t.Name, t.Agent)
		}
		return nil
	}
	for _, sc := range t.Scopes {
		if !p.Has(sc) {
			return fmt.Errorf("%w: token %s holds %s, which you do not; an owner, or beacon token revoke on the host, deletes it", ErrForbidden, t.Name, sc)
		}
	}
	critical := b.d.Store.CriticalMachines()
	if !t.Live(b.now()).Within(p.MachineAccess, func(m string) bool { return critical[m] }) {
		return fmt.Errorf("%w: token %s reaches machines you do not; an owner, or beacon token revoke on the host, deletes it", ErrForbidden, t.Name)
	}
	return nil
}

// revokeToken deletes the token, once whoever asked may.
func (b *Bridge) revokeToken(ctx context.Context, who Actor, name string) error {
	if err := b.d.Store.RevokeToken(name); err != nil {
		return err
	}
	b.d.Store.DeleteConsoleSessions(name)
	b.Recheck()
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "revoke_token", Target: name})
	b.echo(ctx, b.t("%s revoked token %s via %s", who.Name, name, b.viaName(who.Via)))
	return nil
}
