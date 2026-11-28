package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// Who may give machine access (store.MachineAccess). Only a person, within their role, or beacon on the
// host gives access to all machines; a credential gives no more than it has. Only an owner or beacon on
// the host names a critical machine. A change that gives nothing new (it narrows, or keeps what was
// there) is not held to the caller.

// MachineAccessView is a machine access as the API shows it: Access is all, named or none; Machines the
// machines named (with all: the critical machines it also reaches); Until when the access to a named
// machine ends; Summary the same in a sentence.
type MachineAccessView struct {
	Access   string               `json:"machine_access"`
	Machines []string             `json:"machines,omitempty"`
	Until    map[string]time.Time `json:"machine_until,omitempty"`
	Summary  string               `json:"machine_summary"`
}

// MachineAccess is the access the view shows.
func (v MachineAccessView) MachineAccess() store.MachineAccess {
	switch v.Access {
	case store.AccessEvery:
		return store.MachineAccess{Access: store.AccessEvery}
	case store.AccessAll:
		return store.MachineAccess{Access: store.AccessAll, Machines: slices.Clone(v.Machines), MachineUntil: v.Until}
	}
	return store.MachineAccess{Access: store.AccessNamed, Machines: slices.Clone(v.Machines), MachineUntil: v.Until}
}

// AccessView shows a.
func AccessView(a store.MachineAccess) MachineAccessView {
	v := MachineAccessView{Access: a.Access, Machines: slices.Clone(a.Machines), Until: a.MachineUntil, Summary: a.Summary()}
	switch {
	case a.IsEvery():
		v.Access, v.Machines, v.Until = store.AccessEvery, nil, nil
	case a.None():
		v.Access = "none"
	case !a.IsAll():
		v.Access = store.AccessNamed
	}
	return v
}

// cliActor reports whether who is beacon on the host, through the admin socket.
func cliActor(who Actor) bool { return who.Via == "cli" && who.Key == "" }

// withinCaller reports whether who may give an agent these scopes and this machine access. The command
// line may. Anyone else is read afresh: every scope must be one they hold; only a person gives access to
// all machines; only an owner names a critical machine; any other machine named must be enrolled and one
// they reach. It changes nothing.
func (b *Bridge) withinCaller(who Actor, scopes []string, access store.MachineAccess) error {
	if cliActor(who) {
		return nil
	}
	caller, ok := b.caller(who)
	if !ok {
		return fmt.Errorf("%w: you cannot grant a scope you do not hold", ErrForbidden)
	}
	for _, sc := range scopes {
		if !caller.Has(sc) {
			return fmt.Errorf("%w: you cannot grant %s; you do not hold it", ErrForbidden, sc)
		}
	}
	if access.IsAll() && !caller.Person() {
		return fmt.Errorf("%w: only a person, or beacon on the host, gives access to all machines; name the machines instead", ErrForbidden)
	}
	for _, m := range access.Machines {
		switch {
		case caller.Critical(m) && !caller.Owner():
			return fmt.Errorf("%w: %s is a critical machine; only an owner, or beacon on the host, names one", ErrForbidden, m)
		case !caller.Covers(m):
			return fmt.Errorf("%w: you cannot grant machine %s; you do not reach it", ErrForbidden, m)
		}
		if _, enrolled := b.d.Store.Machine(m); !enrolled {
			return fmt.Errorf("%w: no machine is called %s", ErrBadInput, m)
		}
	}
	return nil
}

// grantWidens reports whether the resulting scopes or machine access give an agent something it did not
// already have: a scope, access to all machines, a machine it did not reach, or a longer access to one.
func (b *Bridge) grantWidens(before store.Agent, scopes []string, access store.MachineAccess) bool {
	wanted := before.Wanted()
	for _, sc := range scopes {
		if !slices.Contains(wanted, sc) {
			return true
		}
	}
	critical := b.d.Store.CriticalMachines()
	return !access.Within(before.Live(b.now()), func(m string) bool { return critical[m] })
}

// agentAccess is the machine access an agent is to have: a critical machine newly named gets an end
// store.CriticalGrantTTL from now unless one is given or forever names it; a machine it named already
// keeps its end.
func (b *Bridge) agentAccess(access, before store.MachineAccess, forever []string) store.MachineAccess {
	critical := b.d.Store.CriticalMachines()
	return store.WithCriticalEnds(access, before, func(m string) bool { return critical[m] }, forever, b.now())
}

// accessWords names the machines an access covers, for the chat.
func (b *Bridge) accessWords(a store.MachineAccess) string {
	names := strings.Join(a.Machines, ", ")
	switch {
	case a.IsEvery():
		return b.t("all machines")
	case a.IsAll() && names == "":
		return b.t("all machines that are not critical")
	case a.IsAll():
		return b.t("all machines that are not critical, and critical machines %s", names)
	case names == "":
		return b.t("no machines")
	}
	return names
}

// SetCriticalMachines names the critical machines a person who is not an owner reaches, each until its
// end if it has one; named with no machines takes them all away. Only an owner, or beacon on the host,
// does it. A machine named already keeps its end unless named gives another. What the person and their
// agents have open on machines they no longer reach is closed. Audited and echoed to the chat.
func (b *Bridge) SetCriticalMachines(ctx context.Context, who Actor, username string, named store.MachineAccess) (UserView, error) {
	if !cliActor(who) {
		p, ok := b.caller(who)
		if !ok || !p.Owner() {
			return UserView{}, fmt.Errorf("%w: only an owner, or beacon on the host, names the critical machines a person reaches", ErrForbidden)
		}
	}
	if named.IsAll() {
		return UserView{}, fmt.Errorf("%w: name the critical machines, not all", ErrBadInput)
	}
	for _, m := range named.Machines {
		if _, ok := b.d.Store.Machine(m); !ok {
			return UserView{}, fmt.Errorf("%w: no machine is called %s", ErrBadInput, m)
		}
	}
	current, ok := b.d.Store.Account(username)
	if !ok {
		return UserView{}, fmt.Errorf("%w: no account %q", store.ErrNotFound, username)
	}
	named = store.KeepEnds(named, current.CriticalAccess())
	before, after, err := b.d.Store.SetCriticalMachines(username, named)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return UserView{}, err
	case err != nil:
		return UserView{}, fmt.Errorf("%w: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	was, is := before.CriticalAccess().String(), after.CriticalAccess().String()
	if was != is {
		b.Recheck()
		_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: who.Name, Via: who.Via, Action: "user_machines", Target: username, Detail: was + " -> " + is})
		b.echo(ctx, b.t("%s set the critical machines %s reaches to %s via %s", who.Name, username, b.accessWords(after.CriticalAccess()), b.viaName(who.Via)))
	}
	return b.userView(after), nil
}

// MachineChoice reads a machine access sent to the API or MCP: access is all or named, machines the
// machines named. Neither sent is nil: nothing to change. machines alone names those machines; an empty
// list names none, which reaches no machine. Nothing sent ever means all.
func MachineChoice(access *string, machines *[]string) (*store.MachineAccess, error) {
	if access == nil && machines == nil {
		return nil, nil
	}
	var names []string
	if machines != nil {
		names = *machines
	}
	out := store.NamedAccess(names...)
	if access != nil {
		switch *access {
		case store.AccessAll:
			out = store.AllAccess(names...)
		case store.AccessNamed:
		default:
			return nil, fmt.Errorf("%w: machine_access is %s or %s", ErrBadInput, store.AccessAll, store.AccessNamed)
		}
	}
	for _, m := range out.Machines {
		if !store.MachineName.MatchString(m) {
			return nil, fmt.Errorf("%w: machine %q: names are lower case letters, digits and dashes", ErrBadInput, m)
		}
	}
	return &out, nil
}
