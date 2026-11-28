package sshgate

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/store"
)

// A machine's sshd usually has a host key of several kinds, and offers the one its client prefers. Beacon
// pins one of them, and asks for one of each kind it can pin, so that it can tell the machine from every
// other on record by any of its keys (store.Machine.HostKeys): a second record for the same machine,
// pinned with another of its keys, is refused like one pinned with the same key.

// hostKeyKinds are the kinds of host key Beacon asks a machine for, one handshake each, in the order it
// prefers to pin them.
var hostKeyKinds = [][]string{
	{ssh.KeyAlgoED25519},
	{ssh.KeyAlgoECDSA256},
	{ssh.KeyAlgoECDSA384},
	{ssh.KeyAlgoECDSA521},
	{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256},
}

// hostKeysWait bounds asking a machine for all its host keys.
const hostKeysWait = 30 * time.Second

// hostKeySet is the host keys a machine offered, one of each kind it has, in the order of hostKeyKinds:
// the first is the one Beacon pins.
type hostKeySet []ssh.PublicKey

func authorizedLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// pinned is the key Beacon pins, in the authorized_keys form.
func (s hostKeySet) pinned() string { return authorizedLine(s[0]) }

// lines are the keys in the authorized_keys form, one a line, for store.Machine.HostKeys.
func (s hostKeySet) lines() string {
	out := make([]string, 0, len(s))
	for _, k := range s {
		out = append(out, authorizedLine(k))
	}
	return strings.Join(out, "\n")
}

// fingerprints are the keys' SHA256 fingerprints, the pinned one first, as the audit log has them.
func (s hostKeySet) fingerprints() string {
	out := make([]string, 0, len(s))
	for _, k := range s {
		out = append(out, ssh.FingerprintSHA256(k))
	}
	return strings.Join(out, " ")
}

// has reports whether the set holds key, a host key in the authorized_keys form.
func (s hostKeySet) has(key string) bool {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return false
	}
	for _, x := range s {
		if bytes.Equal(x.Marshal(), k.Marshal()) {
			return true
		}
	}
	return false
}

// probeHostKeys asks the machine for a host key of each kind in hostKeyKinds, each over a connection of its
// own that open gives. A kind the machine has no key of is left out. Any other failure fails it all, so
// that no key the machine has goes unseen, and so does a connection that reaches another address than the
// first did: every key comes from the same place.
func probeHostKeys(open func() (net.Conn, error), addr string) (hostKeySet, error) {
	var keys hostKeySet
	var reached string
	for _, kind := range hostKeyKinds {
		nc, err := open()
		if err != nil {
			return nil, err
		}
		at := nc.RemoteAddr().String()
		if reached == "" {
			reached = at
		}
		if at != reached {
			_ = nc.Close()
			return nil, fmt.Errorf("%s led to %s and then to %s while Beacon asked for its host keys; give the machine's own address", addr, reached, at)
		}
		k, err := probeHostKey(nc, addr, kind)
		_ = nc.Close()
		var none *ssh.AlgorithmNegotiationError
		switch {
		case err == nil:
			keys = append(keys, k)
		case errors.As(err, &none) && none.What == "host key": // no key of this kind
		default:
			return nil, err
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("the machine offered no host key of a kind Beacon pins (Ed25519, ECDSA or RSA)")
	}
	return keys, nil
}

// keyFingerprints are the SHA256 fingerprints of the host keys m is known by, the pinned one first, as
// the audit log has them.
func keyFingerprints(m store.Machine) string {
	var out []string
	for _, line := range m.KnownHostKeys() {
		if fp := fingerprint(line); fp != "" && !slices.Contains(out, fp) {
			out = append(out, fp)
		}
	}
	return strings.Join(out, " ")
}

var errGotKey = errors.New("host key received")

// probeHostKey starts a handshake on nc offering only the host key algorithms given, and stops it as soon
// as the server's host key arrives.
func probeHostKey(nc net.Conn, addr string, algorithms []string) (ssh.PublicKey, error) {
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{User: "beacon",
		HostKeyCallback:   func(_ string, _ net.Addr, k ssh.PublicKey) error { got = k; return errGotKey },
		HostKeyAlgorithms: algorithms}
	_, _, _, err := ssh.NewClientConn(nc, addr, cfg)
	if got != nil {
		return got, nil
	}
	return nil, err
}
