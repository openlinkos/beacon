package sshgate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

func newKey() (ssh.Signer, error) {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(k)
}

// MachineView is a machine as the API shows it.
type MachineView struct {
	Name     string    `json:"name"`
	Address  string    `json:"address"`
	User     string    `json:"user"`
	HostKey  string    `json:"host_key_fingerprint"`
	Critical bool      `json:"critical,omitempty"`
	Note     string    `json:"note,omitempty"`
	Added    time.Time `json:"added"`
	AddedBy  string    `json:"added_by,omitempty"`
	// Reachable says whether the caller's token reaches it through the channel.
	Reachable bool `json:"reachable"`
	// Connector says the machine connects out to the channel itself (behind NAT); Connected and
	// ConnectedSince say whether and since when it is connected right now. Both are left out for
	// machines Beacon dials.
	Connector      bool      `json:"connector,omitempty"`
	Connected      *bool     `json:"connected,omitempty"`
	ConnectedSince time.Time `json:"connected_since,omitzero"`
}

// view is a machine as the API shows it to the holder of t.
func (g *Gate) view(m store.Machine, t store.Token) MachineView {
	v := MachineView{Name: m.Name, Address: m.Address, User: m.User, HostKey: fingerprint(m.HostKey), Critical: m.Critical,
		Note: m.Note, Added: m.Added, AddedBy: m.AddedBy, Reachable: t.Reaches(m.Name)}
	if m.Connector() {
		v.Connector = true
		connected := false
		if c := g.connectors.get(m.Name); c != nil {
			connected, v.ConnectedSince = true, c.since
		}
		v.Connected = &connected
	}
	return v
}

// Machines is the list of machines, and how the caller connects to them with its own ssh.
type Machines struct {
	Machines []MachineView `json:"machines"`
	// SSHConfig is an ssh_config(5) block for the machines the caller reaches, with the channel's host
	// key for known_hosts; empty without Address.
	SSHConfig  string `json:"ssh_config,omitempty"`
	KnownHosts string `json:"known_hosts,omitempty"`
}

func fingerprint(authorized string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(k)
}

// List returns the machines. address is where agents reach the channel (host:port), for the ssh
// configuration; it may be empty.
func (g *Gate) List(who Actor, address string) (Machines, error) {
	if g == nil {
		return Machines{}, ErrDisabled
	}
	t, _ := g.token(who.Name)
	out := Machines{Machines: []MachineView{}}
	ms := g.d.Store.Machines()
	slices.SortFunc(ms, func(a, b store.Machine) int { return strings.Compare(a.Name, b.Name) })
	var cfg strings.Builder
	host, port, _ := net.SplitHostPort(address)
	for _, m := range ms {
		v := g.view(m, t)
		out.Machines = append(out.Machines, v)
		if v.Reachable && host != "" {
			fmt.Fprintf(&cfg, "Host %s\n  HostName %s\n  Port %s\n  User %s\n  HostKeyAlias beacon\n\n", m.Name, host, port, m.Name)
		}
	}
	if host != "" {
		out.SSHConfig = strings.TrimSuffix(cfg.String(), "\n")
		out.KnownHosts = "beacon " + g.HostKey()
	}
	return out, nil
}

// MachineInput adds a machine. Address is host or host:port (22 by default), or "connector" for a
// machine behind NAT that connects out to the channel itself; User defaults to beacon. Replace pins the
// machine's host key again, after it was reinstalled, say. ConnectorKey is the public key a connector
// machine signs in with, and HostKey its sshd's host key, which Beacon otherwise learns the first time
// the machine connects; both are for connector machines only.
type MachineInput struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	User         string `json:"user,omitempty"`
	Critical     bool   `json:"critical,omitempty"`
	Note         string `json:"note,omitempty"`
	Replace      bool   `json:"replace,omitempty"`
	ConnectorKey string `json:"connector_key,omitempty"`
	HostKey      string `json:"host_key,omitempty"`
}

var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

// Added is a machine just added, with what it needs before agents can use it.
type Added struct {
	Machine MachineView `json:"machine"`
	// TrustedUserCAKey is the line for the machine's TrustedUserCAKeys file.
	TrustedUserCAKey string `json:"trusted_user_ca_key"`
	// Connector is the machine-side setup of a machine behind NAT; nil for a machine Beacon dials.
	Connector *ConnectorSetup `json:"connector,omitempty"`
}

// Add pins a machine's host key and adds it.
func (g *Gate) Add(ctx context.Context, who Actor, in MachineInput) (Added, error) {
	if g == nil {
		return Added{}, ErrDisabled
	}
	if !store.MachineName.MatchString(in.Name) {
		return Added{}, fmt.Errorf("%w: a machine's name is its host name: lower case letters, digits and dashes", ErrBadInput)
	}
	if in.User == "" {
		in.User = "beacon"
	}
	if !userRe.MatchString(in.User) {
		return Added{}, fmt.Errorf("%w: user %q is not an account name", ErrBadInput, in.User)
	}
	addr := strings.TrimSpace(in.Address)
	if addr == store.ConnectorAddress {
		return g.addConnector(ctx, who, in)
	}
	if in.ConnectorKey != "" || in.HostKey != "" {
		return Added{}, fmt.Errorf("%w: connector_key and host_key are for a machine behind NAT, added with the address %s; Beacon pins the host key of a machine it dials itself", ErrBadInput, store.ConnectorAddress)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "22")
	}
	if h, _, _ := net.SplitHostPort(addr); h == "" {
		return Added{}, fmt.Errorf("%w: address is a host name or address, optionally with :port", ErrBadInput)
	}
	if old, ok := g.d.Store.Machine(in.Name); ok && !in.Replace {
		return Added{}, fmt.Errorf("%w (%s at %s); add it with replace to pin its host key again", ErrExists, old.Name, old.Address)
	}
	k, err := g.hostKey(ctx, addr)
	if err != nil {
		return Added{}, fmt.Errorf("%w: %s: %v", ErrUnreachable, addr, err) //nolint:errorlint // one wrapped error is enough
	}
	m := store.Machine{Name: in.Name, Address: addr, User: in.User, HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))),
		Critical: in.Critical, Note: strings.TrimSpace(in.Note), Added: g.now().UTC(), AddedBy: who.Name}
	if err := g.d.Store.PutMachine(m); err != nil {
		return Added{}, err
	}
	_ = g.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "machine_add", Target: m.Name, Detail: addr + " " + ssh.FingerprintSHA256(k)})
	g.post(ctx, i18n.T(g.d.Lang, "%s added machine %s (%s)", who.Name, m.Name, addr))
	t, _ := g.token(who.Name)
	return Added{Machine: g.view(m, t), TrustedUserCAKey: g.CAPublicKey()}, nil
}

// Remove forgets a machine; its sessions stay on record. A connector machine's connection is closed.
func (g *Gate) Remove(ctx context.Context, who Actor, name string) error {
	if g == nil {
		return ErrDisabled
	}
	if err := g.d.Store.DeleteMachine(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w called %q", ErrNoMachine, name)
		}
		return err
	}
	g.connectors.close(name)
	_ = g.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "machine_remove", Target: name})
	g.post(ctx, i18n.T(g.d.Lang, "%s removed machine %s", who.Name, name))
	return nil
}

var errGotKey = errors.New("host key received")

// hostKey asks the machine at addr for its host key, preferring Ed25519.
func (g *Gate) hostKey(ctx context.Context, addr string) (ssh.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	nc, err := g.d.Dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = nc.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	return probeHostKey(nc, addr)
}

// probeHostKey starts a handshake on nc and stops it as soon as the server's host key arrives.
func probeHostKey(nc net.Conn, addr string) (ssh.PublicKey, error) {
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{User: "beacon",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error { got = k; return errGotKey },
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
			ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}}
	_, _, _, err := ssh.NewClientConn(nc, addr, cfg)
	if got != nil {
		return got, nil
	}
	return nil, err
}
