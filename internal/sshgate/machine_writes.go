package sshgate

import (
	"fmt"
	"strings"

	"github.com/openlinkos/beacon/internal/store"
)

// Who may change machines. Using a machine and changing it are different things: a credential that
// names the machines it reaches may use them, never add, replace, re-address, re-account or remove a
// machine, or change its connector. Those take machines:write and access to all machines, so that a
// credential limited to some machines cannot point a name it does not reach, or one it does, at another
// host. A critical machine (critical before or after the change) is changed only by an owner or by
// beacon on the host, and so is the mark itself. The account Beacon signs certificates for is changed
// only by a person or beacon on the host, and root only by beacon on the host: it is the account the
// machine's own sudoers cannot bound.

// rootUser is the account only the command line may have Beacon sign certificates for.
const rootUser = "root"

// cli reports whether who is beacon on the host, through the admin socket.
func cli(who Actor) bool { return who.Key == "" && who.Via == "cli" }

// machineChange is what a write does to a machine, for mayChange.
type machineChange struct {
	critical bool // the machine is critical before or after: the mark is set or cleared, or it is critical
	user     bool // the account changes, or a new machine is to be reached as root
	newUser  string
}

// mayChange reports whether who may make the change: nil for the command line, ErrForbidden with the
// reason for anyone else who may not. It reads who afresh.
func (g *Gate) mayChange(who Actor, c machineChange) error {
	if cli(who) {
		return nil
	}
	p, ok := g.principal(who)
	if !ok || !p.Has("machines:write") {
		return fmt.Errorf("%w: changing machines requires the machines:write scope", ErrForbidden)
	}
	if who.Cred != "" && !g.d.Store.CredentialLive(who.Cred, p) {
		return fmt.Errorf("%w: the credential is no longer valid", ErrForbidden)
	}
	if !p.IsAll() {
		reach := strings.Join(p.Machines, ", ")
		if reach == "" {
			reach = "none"
		}
		return fmt.Errorf("%w: changing machines requires access to all machines; this credential reaches only the machines it names (%s)", ErrForbidden, reach)
	}
	if c.critical && !p.Owner() {
		return fmt.Errorf("%w: only an owner, or beacon machine on the host, marks a machine critical or changes a critical one", ErrForbidden)
	}
	if c.user && c.newUser == rootUser {
		return fmt.Errorf("%w: only beacon machine on the host has Beacon sign in to a machine as root", ErrForbidden)
	}
	if c.user && !p.Person() {
		return fmt.Errorf("%w: only a person, or beacon machine on the host, changes the account Beacon signs in to a machine with", ErrForbidden)
	}
	return nil
}

// mayRegister checks a new machine, or one registered again in place of old (exists).
func (g *Gate) mayRegister(who Actor, in MachineInput, old store.Machine, exists bool) error {
	return g.mayChange(who, machineChange{critical: in.Critical || exists && old.Critical,
		user: !exists && in.User == rootUser || exists && old.User != in.User, newUser: in.User})
}

// mayEdit checks a change of a machine's settings from before to after.
func (g *Gate) mayEdit(who Actor, before, after store.Machine) error {
	return g.mayChange(who, machineChange{critical: before.Critical || after.Critical, user: before.User != after.User, newUser: after.User})
}

// TidyMachineAccess forgets the named machines whose access ended, on every credential and account, each
// in the audit log (store.TidyMachineAccess). Their access ended when the time came; Recheck closes
// what is open on them.
func (g *Gate) TidyMachineAccess() int {
	if g == nil {
		return 0
	}
	ended, err := g.d.Store.TidyMachineAccess(g.now())
	if err != nil {
		g.d.Log.Warn("named machines whose access ended could not be tidied; their access has ended all the same", "err", err)
		return 0
	}
	for _, c := range ended {
		g.d.Log.Info("access to named machines ended", "kind", c.Kind, "name", c.Name, "machines", strings.Join(c.Ended, ","), "now", c.Access.String())
	}
	return len(ended)
}
