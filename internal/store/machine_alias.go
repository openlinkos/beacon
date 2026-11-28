package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/kv"
)

// One machine, one record. A machine's access, its critical mark and its account belong to its record,
// so a second record for the same machine would reach it past them: Beacon keeps no two machines dialled
// at the same address, and none that share a host key, the pinned one or another each offered
// (Machine.HostKeys; a connector machine's connector key is its own as well, writeMachine). The checks
// run when a machine is registered and when its host keys or connector key are set, in the same
// transaction.

// ErrMachineAlias is the answer for a machine that would be another machine's second record.
var ErrMachineAlias = errors.New("another machine has this address or host key")

// NormalAddress is a dialled machine's address as compared with another's: the host in lower case and
// without a final dot, an IP address in its canonical form (an IPv4-mapped IPv6 address as IPv4), and
// the port as a number. A connector machine's address is ConnectorAddress itself.
func NormalAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.ToLower(addr)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Is4In6() {
			ip = ip.Unmap()
		}
		host = ip.String()
	}
	if n, err := strconv.Atoi(port); err == nil {
		port = strconv.Itoa(n)
	}
	return net.JoinHostPort(host, port)
}

// hostKeyID is a host key in the authorized_keys form without its comment: its type and its key.
func hostKeyID(key string) string {
	f := strings.Fields(key)
	if len(f) < 2 {
		return strings.Join(f, " ")
	}
	return f[0] + " " + f[1]
}

// KnownHostKeys are the host keys m is known by, each once, in the authorized_keys form: the one pinned
// first, then the others it offered (HostKeys). A machine registered before Beacon asked for them all is
// known by its pinned key alone.
func (m Machine) KnownHostKeys() []string {
	var out, ids []string
	for _, k := range append([]string{m.HostKey}, strings.Split(m.HostKeys, "\n")...) {
		if id := hostKeyID(k); id != "" && !slices.Contains(ids, id) {
			out, ids = append(out, strings.TrimSpace(k)), append(ids, id)
		}
	}
	return out
}

// hostKeyIDs are the host keys m is known by (KnownHostKeys), without comments.
func (m Machine) hostKeyIDs() []string {
	ids := m.KnownHostKeys()
	for i, k := range ids {
		ids[i] = hostKeyID(k)
	}
	return ids
}

// aliasOf reports why m would be other's second record, or "" when it would not.
func aliasOf(m, other Machine) string {
	switch {
	case other.Name == m.Name:
		return ""
	case m.Address != "" && !m.Connector() && !other.Connector() && NormalAddress(m.Address) == NormalAddress(other.Address):
		return fmt.Sprintf("machine %s is dialled at %s", other.Name, other.Address)
	}
	theirs := other.hostKeyIDs()
	for _, id := range m.hostKeyIDs() {
		if slices.Contains(theirs, id) {
			return fmt.Sprintf("machine %s has the host key %s", other.Name, id)
		}
	}
	return ""
}

// machineAlias reports why m would be another machine's second record, or "".
func machineAlias(tx kv.Tx, m Machine) string {
	var why string
	tx.Range(bucketMachines, kv.Range{}, func(_, v []byte) bool {
		var other Machine
		if json.Unmarshal(v, &other) == nil {
			why = aliasOf(m, other)
		}
		return why == ""
	})
	return why
}

// checkAlias refuses m when it would be another machine's second record, unless it keeps the address it
// is stored with already and is known by no host key it was not known by (knownAlready).
func checkAlias(tx kv.Tx, m Machine) error {
	if old, err := machineIn(tx, m.Name); err == nil && old.Address == m.Address && knownAlready(m, old) {
		return nil
	}
	if why := machineAlias(tx, m); why != "" {
		return fmt.Errorf("%w: %s", ErrMachineAlias, why)
	}
	return nil
}

// knownAlready reports whether m keeps the host key old pinned and is known by no other host key old
// was not known by.
func knownAlready(m, old Machine) bool {
	if hostKeyID(m.HostKey) != hostKeyID(old.HostKey) {
		return false
	}
	known := old.hostKeyIDs()
	for _, id := range m.hostKeyIDs() {
		if !slices.Contains(known, id) {
			return false
		}
	}
	return true
}

// MachineAlias reports why m would be another machine's second record, or "" when it would not: for a
// check before a machine is dialled. Writing the machine checks again.
func (s *Store) MachineAlias(m Machine) string {
	var why string
	_ = s.db.View(func(tx kv.Tx) error {
		why = machineAlias(tx, m)
		return nil
	})
	return why
}

// MachineAliases lists the machines that are another's second record already, stored before Beacon
// refused them: each as "a and b: why", comparing every host key each is known by. Registering either
// again takes the other removed first.
func (s *Store) MachineAliases() []string {
	ms := s.Machines()
	var out []string
	for i, m := range ms {
		for _, other := range ms[i+1:] {
			if why := aliasOf(m, other); why != "" {
				out = append(out, m.Name+" and "+other.Name+": "+why)
			}
		}
	}
	slices.Sort(out)
	return out
}
