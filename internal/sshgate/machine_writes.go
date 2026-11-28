package sshgate

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

// Who may change machines. Using a machine and changing it are different things: a credential that
// names the machines it reaches may use them, never add, replace, re-address, re-account or remove a
// machine, or change its connector. Those take machines:write and access to all machines, so that a
// credential limited to some machines cannot point a name it does not reach, or one it does, at another
// host. A critical machine (critical before or after the change) is changed only by an owner or by
// beacon on the host, and so is the mark itself. The account Beacon signs certificates for is changed,
// or a new machine given one other than beacon, only by a person or beacon on the host, and root only by
// beacon on the host: it is the account the machine's own sudoers cannot bound.
//
// One machine has one record: a machine is not added or registered again at another machine's address or
// with its host key (store.ErrMachineAlias), so that a second name cannot reach a machine past its
// critical mark, its account or its access. A machine at a loopback, unspecified or link-local address,
// or at localhost, is added only by beacon on the host: Beacon's own host holds the certificate
// authority. Beacon checks the address it was given and the one it reached. A host key given for a
// connector machine by anyone but a person or beacon on the host is an Ed25519 key, the kind Beacon pins
// when it learns one itself.

// rootUser is the account only the command line may have Beacon sign certificates for.
const rootUser = "root"

// defaultUser is the account a machine is reached as unless whoever adds it says otherwise.
const defaultUser = "beacon"

// ErrAlias is the answer for a machine that would be another machine's second record.
var ErrAlias = errors.New("another machine has this address or host key")

// aliasError says which machine a registration would alias, and what to do.
func aliasError(why string) error {
	return fmt.Errorf("%w: %s; one machine has one record (a machine cloned from another's image needs host keys of its own: ssh-keygen -A after removing /etc/ssh/ssh_host_*)", ErrAlias, why)
}

// localHost reports whether host, a host name or an IP address, is Beacon's own host or its link: a
// loopback, unspecified or link-local address (0.0.0.0/8 included), localhost or a name under .localhost.
func localHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && localIP(ip)
}

func localIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.Is4() && ip.As4()[0] == 0
}

// reachedLocal reports whether the connection Beacon opened to add a machine reached a local address
// (localHost), whatever name it was given.
func reachedLocal(nc net.Conn) bool {
	a, ok := nc.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(a.IP)
	return ok && localIP(ip)
}

// errLocal refuses a machine at a local address to anyone but beacon on the host.
func errLocal(addr string) error {
	return fmt.Errorf("%w: %s is this host's own address or link; only beacon machine on the host adds a machine there", ErrForbidden, addr)
}

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
		return fmt.Errorf("%w: only a person, or beacon machine on the host, gives a machine an account other than %s or changes the account Beacon signs in to a machine with", ErrForbidden, defaultUser)
	}
	return nil
}

// mayRegister checks a new machine, or one registered again in place of old (exists): a new machine's
// account other than beacon is a change of account, as is another account for one registered again.
func (g *Gate) mayRegister(who Actor, in MachineInput, old store.Machine, exists bool) error {
	return g.mayChange(who, machineChange{critical: in.Critical || exists && old.Critical,
		user: !exists && in.User != defaultUser || exists && old.User != in.User, newUser: in.User})
}

// mayGiveHostKey checks a host key given for a connector machine: anything but Ed25519 takes a person or
// beacon on the host. Beacon pins an Ed25519 key when it learns one itself, as it does for every machine
// it dials; a key of another kind may be one no other record shows, of a machine recorded already.
func (g *Gate) mayGiveHostKey(who Actor, key string) error {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return fmt.Errorf("%w: host_key: %w", ErrBadInput, err)
	}
	if k.Type() == ssh.KeyAlgoED25519 || cli(who) {
		return nil
	}
	if p, ok := g.principal(who); ok && p.Person() {
		return nil
	}
	return fmt.Errorf("%w: give the machine's Ed25519 host key (/etc/ssh/ssh_host_ed25519_key.pub), or leave it out for Beacon to learn; only a person, or beacon machine on the host, gives a %s key", ErrForbidden, k.Type())
}

// registrationError is a store error from registering a machine, as the channel answers it.
func registrationError(name string, err error) error {
	var why string
	if errors.Is(err, store.ErrMachineAlias) {
		why = strings.TrimPrefix(err.Error(), store.ErrMachineAlias.Error()+": ")
		return aliasError(why)
	}
	return machineVersionError(name, err)
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
