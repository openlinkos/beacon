package sshgate

// Machines behind NAT connect out to the channel themselves, with the OpenSSH client they already have:
//
//	ssh -N -R 0:localhost:22 <machine>@<beacon>:17002
//
// signing in as the machine (the user name is the machine's name, as agents name their targets) with a
// dedicated key registered on the machine's record (store.Machine.ConnectorKey). Such a connection may do
// nothing but ask for that one remote forward and keep alive: no sessions, no channels, no other
// requests. Beacon binds no port for the forward; it records that the machine is reachable through this
// connection and, when an agent targets the machine, opens a forwarded-tcpip channel back to the address
// the machine itself forwarded, never one of Beacon's choosing, and runs its usual client handshake over
// it, with the pinned host key and a certificate. One connection per machine; the newest wins.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/store"
)

// ErrDisconnected is the answer for a connector machine whose connection is not up.
var ErrDisconnected = errors.New("the machine's connector is not connected; it connects out to the channel with ssh -R")

const (
	connectorForwardWait = 30 * time.Second // a connector must ask for its forward within this
	connectorKeepalive   = 30 * time.Second // Beacon's own keepalives to a connector
	connectorDeadAfter   = 90 * time.Second // a connector that answers none for this long is closed
	// defaultForwardPort is what Beacon tells a connector that asked for port 0. Nothing listens on it:
	// it is only the label both sides use for the forward in forwarded-tcpip opens.
	defaultForwardPort = 22
	connectorKeyPath   = "/etc/ssh/beacon_connect_key"
	connectorHostsPath = "/etc/ssh/beacon_known_hosts"
	connectorUser      = "beacon-connector"
)

// connector is one machine's connection out to the channel.
type connector struct {
	g     *Gate
	name  string
	sc    *ssh.ServerConn
	since time.Time
	from  string

	mu        sync.Mutex
	addr      string // the address the machine asked Beacon to bind, echoed in every forwarded-tcpip open
	port      uint32 // the port Beacon told it
	forwarded bool
	learnMu   sync.Mutex // one host key probe at a time
}

func (c *connector) setForward(addr string, port uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addr, c.port, c.forwarded = addr, port, true
}

func (c *connector) forward() (string, uint32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addr, c.port, c.forwarded
}

// connectorSet is the connectors connected right now, by machine.
type connectorSet struct {
	mu sync.Mutex
	by map[string]*connector
}

// put makes c the machine's connector and returns the one it replaces, if any.
func (s *connectorSet) put(name string, c *connector) *connector {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.by[name]
	s.by[name] = c
	return old
}

// remove forgets c, unless a newer connector took its place.
func (s *connectorSet) remove(name string, c *connector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by[name] == c {
		delete(s.by, name)
	}
}

func (s *connectorSet) get(name string) *connector {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.by[name]
}

// close closes the machine's connector, if one is connected.
func (s *connectorSet) close(name string) {
	if c := s.get(name); c != nil {
		_ = c.sc.Close()
	}
}

func (s *connectorSet) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.by)
}

func (g *Gate) registerConnectorMetrics(m *metrics.Metrics) {
	m.Register("beacon_ssh_connectors", "Connector machines (behind NAT) connected out to the SSH channel right now", "gauge", func() []metrics.Sample {
		return []metrics.Sample{{Value: float64(g.connectors.count())}}
	})
	m.Register("beacon_ssh_connector_machines", "Machines added as connectors, connected or not", "gauge", func() []metrics.Sample {
		n := 0
		for _, m := range g.d.Store.Machines() {
			if m.Connector() {
				n++
			}
		}
		return []metrics.Sample{{Value: float64(n)}}
	})
}

// publicKey authenticates a connection to the channel by its key: an agent's token, or a machine's
// connector key, which only signs in as that machine. A key a token holds is a token's key, whatever a
// machine says: a token's key can never register a forward, a connector key can never open a session.
func (g *Gate) publicKey(cm ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
	fp := ssh.FingerprintSHA256(k)
	if t, ok := g.d.Store.TokenBySSHKey(fp); ok {
		if !t.Has("machines:ssh") {
			return nil, errors.New("unknown key")
		}
		return &ssh.Permissions{Extensions: map[string]string{"token": t.Name}}, nil
	}
	if m, ok := g.d.Store.MachineByConnectorKey(fp); ok && m.Name == cm.User() {
		return &ssh.Permissions{Extensions: map[string]string{"connector": m.Name}}, nil
	}
	return nil, errors.New("unknown key")
}

// serveConnector handles a machine's connection out to the channel until it ends.
func (g *Gate) serveConnector(sc *ssh.ServerConn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request, name string) {
	c := &connector{g: g, name: name, sc: sc, since: g.now().UTC(), from: sc.RemoteAddr().String()}
	go func() { // a connector carries no channels: no sessions, no forwarded ports of its own
		for ch := range chans {
			_ = ch.Reject(ssh.Prohibited, "a connector connection carries no channels; agents reach the machine through Beacon")
		}
	}()
	noForward := time.AfterFunc(connectorForwardWait, func() {
		if _, _, ok := c.forward(); !ok {
			g.d.Log.Warn("connector asked for no forward; closed", "machine", name, "from", c.from)
			_ = sc.Close()
		}
	})
	defer noForward.Stop()
	registered := false
	defer func() {
		if registered {
			g.connectors.remove(name, c)
			g.d.Log.Info("connector disconnected", "machine", name, "from", c.from, "after", g.now().Sub(c.since).Round(time.Second).String())
		}
	}()
	for r := range reqs {
		switch r.Type {
		case "tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			if _, _, done := c.forward(); done || ssh.Unmarshal(r.Payload, &p) != nil {
				if r.WantReply {
					_ = r.Reply(false, []byte("one forward per connection"))
				}
				continue
			}
			port := p.Port
			if port == 0 {
				port = defaultForwardPort
			}
			c.setForward(p.Addr, port)
			if r.WantReply {
				var reply []byte
				if p.Port == 0 { // RFC 4254, 7.1: the reply names the port bound, only when 0 was asked
					reply = ssh.Marshal(struct{ Port uint32 }{port})
				}
				_ = r.Reply(true, reply)
			}
			if old := g.connectors.put(name, c); old != nil {
				g.d.Log.Info("connector replaced by a newer connection", "machine", name, "old", old.from, "new", c.from)
				_ = old.sc.Close()
			}
			registered = true
			g.d.Log.Info("connector connected", "machine", name, "from", c.from, "forward", fmt.Sprintf("%s:%d", p.Addr, p.Port))
			go c.keepalive()
			if m, ok := g.d.Store.Machine(name); ok && m.HostKey == "" {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := g.learnHostKey(ctx, c); err != nil {
						g.d.Log.Warn("connector machine's host key could not be learned", "machine", name, "err", err)
					}
				}()
			}
		case "cancel-tcpip-forward":
			if r.WantReply {
				_ = r.Reply(true, nil)
			}
			_ = sc.Close() // the machine withdrew its forward: as far as agents are concerned it is gone
		default:
			if r.WantReply {
				_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}
}

// keepalive asks the connector every connectorKeepalive whether it is still there (OpenSSH answers any
// global request) and closes a connection that stops answering, so a machine that fell off the network
// shows as disconnected rather than hanging every agent that targets it.
func (c *connector) keepalive() {
	done := make(chan struct{})
	go func() { _ = c.sc.Wait(); close(done) }()
	t := time.NewTicker(connectorKeepalive)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			answered := make(chan struct{})
			go func() { _, _, _ = c.sc.SendRequest("keepalive@openssh.com", true, nil); close(answered) }()
			select {
			case <-answered:
			case <-done:
				return
			case <-time.After(connectorDeadAfter):
				c.g.d.Log.Warn("connector stopped answering; closed", "machine", c.name, "from", c.from)
				_ = c.sc.Close()
				return
			}
		}
	}
}

// open opens a connection to the machine's sshd through the connector: a forwarded-tcpip channel to the
// address the machine itself forwarded.
func (c *connector) open() (net.Conn, error) {
	addr, port, ok := c.forward()
	if !ok {
		return nil, errors.New("the connector has not forwarded a port yet")
	}
	origin := struct {
		IP   string
		Port uint32
	}{"0.0.0.0", 1} // the originator is Beacon itself: its end of the connector's connection
	if la, ok := c.sc.LocalAddr().(*net.TCPAddr); ok && la.Port > 0 {
		origin.IP, origin.Port = la.IP.String(), uint32(la.Port)
	}
	payload := ssh.Marshal(struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{addr, port, origin.IP, origin.Port})
	ch, reqs, err := c.sc.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return &chanConn{Channel: ch, name: c.name}, nil
}

// probe asks the machine's sshd, through the connector, for its host key.
func (c *connector) probe(ctx context.Context) (ssh.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	nc, err := c.open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = nc.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	return probeHostKey(nc, c.name)
}

// learnHostKey pins the machine's host key through its connector the first time it connects: the
// connector signed in with the key the operator installed on the machine, so the sshd it forwards is the
// machine's own. It is audited and echoed to the chat, for people to confirm; -host-key at machine add
// avoids trusting that first connection.
func (g *Gate) learnHostKey(ctx context.Context, c *connector) (string, error) {
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	m, ok := g.d.Store.Machine(c.name)
	if !ok {
		return "", ErrNoMachine
	}
	if m.HostKey != "" {
		return m.HostKey, nil
	}
	k, err := c.probe(ctx)
	if err != nil {
		return "", err
	}
	m.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
	if err := g.d.Store.PutMachine(m); err != nil {
		return "", err
	}
	fp := ssh.FingerprintSHA256(k)
	g.d.Log.Info("connector machine's host key pinned", "machine", c.name, "fingerprint", fp)
	_ = g.d.Store.Audit(store.AuditEntry{Actor: "beacon", Via: "ssh", Action: "machine_host_key", Target: c.name, Detail: fp})
	g.post(ctx, i18n.T(g.d.Lang, "Beacon pinned the host key of machine %s through its connector: %s", c.name, fp))
	return m.HostKey, nil
}

// connect returns a connection to a connector machine's sshd, learning its host key first if it is not
// pinned yet. It fails fast when the connector is not connected.
func (g *Gate) connect(ctx context.Context, m *store.Machine) (net.Conn, error) {
	c := g.connectors.get(m.Name)
	if c == nil {
		return nil, fmt.Errorf("%w (%s)", ErrDisconnected, m.Name)
	}
	if m.HostKey == "" {
		k, err := g.learnHostKey(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: its host key could not be learned: %v", ErrUnreachable, m.Name, err) //nolint:errorlint // one wrapped error is enough
		}
		m.HostKey = k
	}
	nc, err := c.open()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: the connector refused the forwarded connection: %v", ErrUnreachable, m.Name, err) //nolint:errorlint // one wrapped error is enough
	}
	return nc, nil
}

// chanConn is a forwarded-tcpip channel as a net.Conn, for the client handshake with the machine's
// sshd. A deadline closes the channel when it passes, which is what a stuck handshake needs.
type chanConn struct {
	ssh.Channel
	name  string
	mu    sync.Mutex
	timer *time.Timer
}

type sshAddr string

func (a sshAddr) Network() string { return "ssh" }
func (a sshAddr) String() string  { return string(a) }

func (c *chanConn) LocalAddr() net.Addr  { return sshAddr("beacon") }
func (c *chanConn) RemoteAddr() net.Addr { return sshAddr(c.name) }

func (c *chanConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if !t.IsZero() {
		c.timer = time.AfterFunc(time.Until(t), func() { _ = c.Channel.Close() })
	}
	return nil
}

func (c *chanConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *chanConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

func (c *chanConn) Close() error {
	_ = c.SetDeadline(time.Time{})
	return c.Channel.Close()
}

// ---- adding connector machines and their keys

// parseConnectorKey reads the public key a machine connects out with and refuses one a token holds.
func (g *Gate) parseConnectorKey(line string) (key, fingerprint string, err error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return "", "", fmt.Errorf("%w: connector_key is not a public key in the authorized_keys form (ssh-ed25519 AAAA…): %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	if _, isCert := k.(*ssh.Certificate); isCert {
		return "", "", fmt.Errorf("%w: connector_key is a certificate; give the public key itself", ErrBadInput)
	}
	fp := ssh.FingerprintSHA256(k)
	if t, held := g.d.Store.TokenBySSHKey(fp); held {
		return "", "", fmt.Errorf("%w: the key %s belongs to token %s; a machine's connector key is its own", ErrBadInput, fp, t.Name)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), fp, nil
}

func parseHostKey(line string) (string, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return "", fmt.Errorf("%w: host_key is not a public key in the authorized_keys form (the machine's /etc/ssh/ssh_host_ed25519_key.pub): %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	if _, isCert := k.(*ssh.Certificate); isCert {
		return "", fmt.Errorf("%w: host_key is a certificate; give the host key itself", ErrBadInput)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), nil
}

// addConnector adds a machine that connects out to the channel. Its host key is given, or learned the
// first time it connects with the key registered here.
func (g *Gate) addConnector(ctx context.Context, who Actor, in MachineInput) (Added, error) {
	old, exists := g.d.Store.Machine(in.Name)
	if exists && !in.Replace {
		return Added{}, fmt.Errorf("%w (%s at %s); add it with replace to change its keys", ErrExists, old.Name, old.Address)
	}
	m := store.Machine{Name: in.Name, Address: store.ConnectorAddress, User: in.User, Critical: in.Critical,
		Note: strings.TrimSpace(in.Note), Added: g.now().UTC(), AddedBy: who.Name}
	if in.ConnectorKey != "" {
		key, fp, err := g.parseConnectorKey(in.ConnectorKey)
		if err != nil {
			return Added{}, err
		}
		m.ConnectorKey, m.ConnectorFingerprint = key, fp
	} else if exists && old.Connector() {
		m.ConnectorKey, m.ConnectorFingerprint = old.ConnectorKey, old.ConnectorFingerprint
	}
	if in.HostKey != "" {
		k, err := parseHostKey(in.HostKey)
		if err != nil {
			return Added{}, err
		}
		m.HostKey = k
	}
	keyChanged := exists && old.ConnectorFingerprint != m.ConnectorFingerprint
	if m.HostKey == "" && !keyChanged { // connected already, with the key being registered: pin the host key now
		if c := g.connectors.get(in.Name); c != nil {
			if k, err := c.probe(ctx); err == nil {
				m.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
			}
		}
	}
	if err := g.d.Store.PutMachine(m); err != nil {
		return Added{}, err
	}
	if keyChanged { // the connection signed in with the old key ends; the machine reconnects with the new one
		g.connectors.close(in.Name)
	}
	detail := "connector " + m.ConnectorFingerprint
	if m.HostKey != "" {
		detail += " " + fingerprint(m.HostKey)
	}
	_ = g.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "machine_add", Target: m.Name, Detail: strings.TrimSpace(detail)})
	g.post(ctx, i18n.T(g.d.Lang, "%s added machine %s (%s)", who.Name, m.Name, store.ConnectorAddress))
	t, _ := g.token(who.Name)
	return Added{Machine: g.view(m, t), TrustedUserCAKey: g.CAPublicKey(), Connector: g.connectorSetup(m)}, nil
}

// SetConnector sets or changes the key a connector machine connects out with. A connection signed in
// with the old key is closed.
func (g *Gate) SetConnector(ctx context.Context, who Actor, name, key string) (MachineView, error) {
	if g == nil {
		return MachineView{}, ErrDisabled
	}
	m, ok := g.d.Store.Machine(name)
	if !ok {
		return MachineView{}, fmt.Errorf("%w called %q", ErrNoMachine, name)
	}
	if !m.Connector() {
		return MachineView{}, fmt.Errorf("%w: %s is dialled at %s, not a connector machine; add it with the address %s to make it one", ErrBadInput, name, m.Address, store.ConnectorAddress)
	}
	k, fp, err := g.parseConnectorKey(key)
	if err != nil {
		return MachineView{}, err
	}
	changed := m.ConnectorFingerprint != fp
	m.ConnectorKey, m.ConnectorFingerprint = k, fp
	if err := g.d.Store.PutMachine(m); err != nil {
		return MachineView{}, err
	}
	if changed {
		g.connectors.close(name)
	}
	_ = g.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "machine_connector", Target: name, Detail: fp})
	g.post(ctx, i18n.T(g.d.Lang, "%s set the connector key of machine %s", who.Name, name))
	t, _ := g.token(who.Name)
	return g.view(m, t), nil
}

// ConnectorSetup is what a machine behind NAT needs on its side, as machine add prints it.
type ConnectorSetup struct {
	// KnownHosts is the line for the machine's known_hosts file: the channel's host key at its public address.
	KnownHosts string `json:"known_hosts"`
	// Command is the ssh command that connects out; Unit is a systemd unit that keeps it running.
	Command string `json:"command"`
	Unit    string `json:"unit"`
	// Script does it all, as root on the machine: the certificate authority, the account, the known_hosts
	// line, the connector's key and user, the unit.
	Script string `json:"script"`
	// HostKeyPending says the machine's host key is not pinned yet: Beacon learns it when the connector
	// first connects, and says so in the audit log and the chat.
	HostKeyPending bool `json:"host_key_pending,omitempty"`
}

// connectorSetup renders the machine-side setup for m.
func (g *Gate) connectorSetup(m store.Machine) *ConnectorSetup {
	addr := g.d.PublicAddress
	var note string
	if addr == "" {
		addr = "BEACON_ADDRESS:17002"
		note = "# set ssh.public_address in Beacon's configuration to have its address filled in here\n"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = addr, "22"
	}
	kh := knownhosts.Line([]string{knownhosts.Normalize(addr)}, g.d.HostKey.PublicKey())
	cmd := fmt.Sprintf("/usr/bin/ssh -N -R 0:localhost:22 -p %s -i %s -F none -o UserKnownHostsFile=%s -o StrictHostKeyChecking=yes"+
		" -o IdentitiesOnly=yes -o BatchMode=yes -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 %s@%s",
		port, connectorKeyPath, connectorHostsPath, m.Name, host)
	unit := "[Unit]\n" +
		"Description=Beacon connector: connects out to the SSH channel so agents can reach this machine\n" +
		"Documentation=https://github.com/openlinkos/beacon/blob/main/docs/ssh.md\n" +
		"After=network-online.target\nWants=network-online.target\nStartLimitIntervalSec=0\n\n" +
		"[Service]\nUser=" + connectorUser + "\nExecStart=" + cmd + "\nRestart=always\nRestartSec=5\n" +
		"NoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\n\n" +
		"[Install]\nWantedBy=multi-user.target\n"
	var sb strings.Builder
	sb.WriteString(note)
	fmt.Fprintf(&sb, "echo '%s' > /etc/ssh/beacon_ca.pub\n", g.CAPublicKey())
	sb.WriteString("echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf\n")
	sb.WriteString("echo 'PerSourcePenaltyExemptList 127.0.0.1,::1' >> /etc/ssh/sshd_config.d/beacon.conf\n")
	fmt.Fprintf(&sb, "id %s >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' %s\n", m.User, m.User)
	sb.WriteString("systemctl reload ssh\n")
	fmt.Fprintf(&sb, "echo '%s' > %s\n", kh, connectorHostsPath)
	fmt.Fprintf(&sb, "id %s >/dev/null 2>&1 || useradd -r -M -s /usr/sbin/nologin %s\n", connectorUser, connectorUser)
	fmt.Fprintf(&sb, "[ -f %s ] || ssh-keygen -t ed25519 -N '' -C 'beacon connector %s' -f %s\n", connectorKeyPath, m.Name, connectorKeyPath)
	fmt.Fprintf(&sb, "chown %s %s && chmod 600 %s\n", connectorUser, connectorKeyPath, connectorKeyPath)
	fmt.Fprintf(&sb, "cat > /etc/systemd/system/beacon-connector.service <<'EOF'\n%sEOF\n", unit)
	sb.WriteString("systemctl daemon-reload && systemctl enable --now beacon-connector\n")
	if m.ConnectorKey == "" {
		fmt.Fprintf(&sb, "# then register the key with Beacon: beacon machine connector %s -key \"$(cat %s.pub)\"\n", m.Name, connectorKeyPath)
	}
	return &ConnectorSetup{KnownHosts: kh, Command: cmd, Unit: unit, Script: sb.String(), HostKeyPending: m.HostKey == ""}
}
