// Package sshgate is the SSH channel: agents reach the machines Beacon knows with the tools they already
// use (ssh, scp, sftp, rsync, port forwarding) or with the MCP and REST tools, and never hold a key.
// Beacon signs a certificate valid for a few minutes for every connection to a machine, which trusts only
// Beacon's certificate authority, and records every session: asciicast v3 for replay, and a transcript
// that breaks an interactive shell into commands with their exit status.
//
// It does not judge what runs. Reviewing commands is the agents' business, done by their own harness;
// people see every session, echoed to the alert chat, and can pause the channel.
package sshgate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/scrub"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

// Poster sends a plain message to the alert chat.
type Poster interface {
	Send(ctx context.Context, text string) error
}

// Deps are what the channel needs. Bot and Metrics may be nil.
type Deps struct {
	Store   *store.Store
	HostKey ssh.Signer // the channel's own host key, which agents' ssh clients pin
	CA      ssh.Signer // signs the certificates machines accept
	// SourceAddresses, when set, go into every certificate as its source-address option (CIDRs), so a
	// certificate works only from Beacon's own addresses.
	SourceAddresses []string
	Dir             string        // where recordings and transcripts are kept
	Keep            time.Duration // how long sessions are kept
	Bot             Poster
	Lang            i18n.Lang
	Metrics         *metrics.Metrics
	Log             *slog.Logger
	Now             func() time.Time
	// Dial connects to machines; nil uses a net.Dialer. Tests replace it.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// PublicAddress (host:port) is where agents and connector machines reach the channel; it goes into
	// the setup machine add prints for a machine behind NAT. May be empty.
	PublicAddress string
	// Pages builds the signed links to sessions' pages, where people replay them; nil: no links.
	Pages *link.Pages
	// Scrub gives the scrubber of the values a machine may hold — the items bound to it, or with a
	// file declared on it — for the file tools' answers (read_file, and a file read an edit makes);
	// nil: nothing is scrubbed.
	Scrub func(machine string) *scrub.Scrubber
}

// Gate is the SSH channel.
type Gate struct {
	d   Deps
	key ssh.Signer // the throwaway key every certificate is issued for

	mu    sync.Mutex
	open  map[uint64]tracked // what is open on the channel: agent connections, tool calls
	seq   atomic.Uint64
	fails map[string]*window
	echo  *echoer

	connectors *connectorSet // machines behind NAT connected out to the channel (connector.go)
}

type window struct {
	n     int
	start time.Time
}

// Actor is who uses the channel: Name as the audit log names it (a person, "person / agent", or a token's
// name), how (ssh, mcp, api, console or cli), and Key, the principal (store.Principal.Key) whose access
// is checked afresh each time; none for the command line.
type Actor struct {
	Name string
	Via  string
	Key  string
	Cred string // the credential it came with: token:NAME, grant:ID or key:FINGERPRINT; none for the console and the command line
}

// Errors the API and MCP turn into answers. They are wrapped with details.
var (
	ErrDisabled    = errors.New("the SSH channel is not configured")
	ErrPaused      = errors.New("agents' access to machines is paused")
	ErrNoMachine   = errors.New("no such machine")
	ErrForbidden   = errors.New("this agent may not reach the machine")
	ErrUnreachable = errors.New("the machine cannot be reached")
	ErrExists      = errors.New("a machine with this name exists")
	ErrBadInput    = errors.New("bad input")
	ErrNoFile      = errors.New("no such file")
	ErrNotRegular  = errors.New("not a regular file")
	ErrBinary      = errors.New("the file is binary")
	ErrTooLarge    = errors.New("the file is too large")
	ErrChanged     = errors.New("the file changed since it was read")
	ErrNoMatch     = errors.New("old_string is not in the file")
	ErrAmbiguous   = errors.New("old_string occurs more than once")
	ErrNoSession   = errors.New("no such session")
)

// PauseKey is where the start of a pause is stored; zero when the channel runs.
const PauseKey = "ssh_paused"

const (
	certValidity = 5 * time.Minute
	maxFails     = 20
	failWindow   = 10 * time.Minute
)

// New creates the channel.
func New(d Deps) (*Gate, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Dial == nil {
		var nd net.Dialer
		d.Dial = nd.DialContext
	}
	k, err := newKey()
	if err != nil {
		return nil, err
	}
	g := &Gate{d: d, key: k, open: map[uint64]tracked{}, fails: map[string]*window{}, connectors: &connectorSet{by: map[string]*connector{}}}
	g.echo = &echoer{g: g, pending: map[string]*batch{}}
	if m := d.Metrics; m != nil {
		g.registerConnectorMetrics(m)
		m.Register("beacon_ssh_open", "Agent connections and tool calls open on the SSH channel", "gauge", func() []metrics.Sample {
			g.mu.Lock()
			defer g.mu.Unlock()
			return []metrics.Sample{{Value: float64(len(g.open))}}
		})
		m.Register("beacon_ssh_paused", "1 while agents' access to machines is paused", "gauge", func() []metrics.Sample {
			return []metrics.Sample{{Value: map[bool]float64{true: 1, false: 0}[g.Paused()]}}
		})
		m.Register("beacon_ssh_machines", "Machines the SSH channel reaches", "gauge", func() []metrics.Sample {
			return []metrics.Sample{{Value: float64(len(d.Store.Machines()))}}
		})
	}
	return g, nil
}

func (g *Gate) now() time.Time { return g.d.Now() }

// CAPublicKey is the line machines put in their TrustedUserCAKeys file.
func (g *Gate) CAPublicKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(g.d.CA.PublicKey()))) + " beacon-ca"
}

// HostKey is the channel's own host key, for agents' known_hosts.
func (g *Gate) HostKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(g.d.HostKey.PublicKey())))
}

// newID makes a session or connection id that sorts by time.
func newID(t time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return t.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// tracked is something open on the channel: how to close it, who has it open and on which machine.
type tracked struct {
	close   func()
	who     Actor
	machine string
}

// track remembers how to close something who has open on machine, until the returned function is called.
func (g *Gate) track(closer func(), who Actor, machine string) func() {
	id := g.seq.Add(1)
	g.mu.Lock()
	g.open[id] = tracked{close: closer, who: who, machine: machine}
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		delete(g.open, id)
		g.mu.Unlock()
	}
}

// Recheck closes at once what is open on the channel (connections and tool calls) for whoever may no
// longer reach its machine: a token revoked or expired, an agent deleted or given fewer machines or a
// weaker preset, an OAuth grant revoked, an account disabled or demoted so that its agents lose
// machines:ssh, a key removed. Beacon calls it after each such change, and every minute (Tidy) for what
// expired. It reports how many it closed.
func (g *Gate) Recheck() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	items := make([]tracked, 0, len(g.open))
	for _, t := range g.open {
		items = append(items, t)
	}
	g.mu.Unlock()
	n := 0
	for _, t := range items {
		if !g.still(t.who, t.machine) {
			t.close()
			n++
		}
	}
	if n > 0 {
		g.d.Log.Info("closed sessions on the SSH channel whose access ended", "count", n)
	}
	return n
}

// still reports whether who may still reach machine with the credential it came with.
func (g *Gate) still(who Actor, machine string) bool {
	if who.Key == "" { // the command line
		return true
	}
	p, ok := g.principal(who)
	if !ok || !p.Reaches(machine) {
		return false
	}
	return who.Cred == "" || g.d.Store.CredentialLive(who.Cred, p)
}

// Paused reports whether agents' access is paused.
func (g *Gate) Paused() bool { return g != nil && !g.d.Store.Time(PauseKey).IsZero() }

// PausedSince is when the pause started; zero while the channel runs.
func (g *Gate) PausedSince() time.Time {
	if g == nil {
		return time.Time{}
	}
	return g.d.Store.Time(PauseKey)
}

// SetPaused pauses or resumes agents' access. Pausing closes every open connection and tool call.
func (g *Gate) SetPaused(ctx context.Context, who Actor, paused bool) error {
	if g == nil {
		return ErrDisabled
	}
	var t time.Time
	if paused {
		t = g.now()
	}
	if err := g.d.Store.SetTime(PauseKey, t); err != nil {
		return err
	}
	action := "ssh_resume"
	if paused {
		action = "ssh_pause"
		g.mu.Lock()
		closers := make([]func(), 0, len(g.open))
		for _, c := range g.open {
			closers = append(closers, c.close)
		}
		g.mu.Unlock()
		for _, c := range closers {
			c()
		}
	}
	_ = g.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: action})
	if paused {
		g.post(ctx, i18n.T(g.d.Lang, "%s paused agents' access to machines", who.Name))
	} else {
		g.post(ctx, i18n.T(g.d.Lang, "%s resumed agents' access to machines", who.Name))
	}
	return nil
}

func (g *Gate) post(ctx context.Context, text string) {
	if g.d.Bot == nil {
		return
	}
	if err := g.d.Bot.Send(ctx, text); err != nil {
		g.d.Log.Warn("echo to the alert chat failed", "err", err)
	}
}

// principal resolves who, afresh: what it may do now.
func (g *Gate) principal(who Actor) (store.Principal, bool) {
	if who.Key == "" {
		return store.Principal{}, false
	}
	return g.d.Store.PrincipalWith(who.Key, who.Cred)
}

// reachable lists the machines the principal may reach.
func (g *Gate) reachable(t store.Principal) []string {
	var out []string
	for _, m := range g.d.Store.Machines() {
		if t.Reaches(m.Name) {
			out = append(out, m.Name)
		}
	}
	slices.Sort(out)
	return out
}

// machineFor checks that who may reach the machine now and returns it.
func (g *Gate) machineFor(who Actor, name string) (store.Machine, error) {
	if g == nil {
		return store.Machine{}, ErrDisabled
	}
	if g.Paused() {
		return store.Machine{}, fmt.Errorf("%w since %s; a person resumes it with beacon ssh resume", ErrPaused,
			g.PausedSince().Format(time.RFC3339))
	}
	t, ok := g.principal(who)
	if !ok || !t.Has("machines:ssh") {
		return store.Machine{}, fmt.Errorf("%w: the agent lacks the machines:ssh scope", ErrForbidden)
	}
	m, found := g.d.Store.Machine(name)
	if !found || !t.Reaches(name) {
		list := strings.Join(g.reachable(t), ", ")
		if list == "" {
			list = "none"
		}
		if !found {
			return store.Machine{}, fmt.Errorf("%w called %q; machines this agent reaches: %s", ErrNoMachine, name, list)
		}
		return store.Machine{}, fmt.Errorf("%w %s; machines this agent reaches: %s", ErrForbidden, name, list)
	}
	return m, nil
}

// cert issues the certificate for one connection to a machine.
func (g *Gate) cert(m store.Machine, who Actor, conn string) (ssh.Signer, error) {
	now := g.now()
	c := &ssh.Certificate{
		Key:             g.key.PublicKey(),
		Serial:          uint64(now.UnixNano()),
		CertType:        ssh.UserCert,
		KeyId:           fmt.Sprintf("beacon %s for %s via %s", conn, who.Name, who.Via),
		ValidPrincipals: []string{m.User},
		ValidAfter:      uint64(now.Add(-time.Minute).Unix()),
		ValidBefore:     uint64(now.Add(certValidity).Unix()),
		Permissions:     ssh.Permissions{Extensions: map[string]string{"permit-pty": "", "permit-port-forwarding": ""}},
	}
	if len(g.d.SourceAddresses) > 0 {
		src := g.d.SourceAddresses
		if m.Connector() { // its sshd sees the connection come from the connector on the machine itself
			src = append(slices.Clone(src), "127.0.0.1/32", "::1/128")
		}
		c.CriticalOptions = map[string]string{"source-address": strings.Join(src, ",")}
	}
	if err := c.SignCert(rand.Reader, g.d.CA); err != nil {
		return nil, err
	}
	return ssh.NewCertSigner(c, g.key)
}

// algorithms are the host key algorithms that match a pinned key, so the handshake offers that key.
func algorithms(k ssh.PublicKey) []string {
	if k.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{k.Type()}
}

// dial connects to a machine as who: to its address, or through its connector for a machine behind NAT.
func (g *Gate) dial(ctx context.Context, m store.Machine, who Actor, conn string) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var nc net.Conn
	var err error
	if m.Connector() {
		nc, err = g.connect(ctx, &m) // fails fast when the connector is not connected
	} else {
		nc, err = g.d.Dial(ctx, "tcp", m.Address)
		if err != nil {
			err = fmt.Errorf("%w: %s: %v", ErrUnreachable, m.Name, err) //nolint:errorlint // one wrapped error is enough
		}
	}
	if err != nil {
		return nil, err
	}
	hk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(m.HostKey))
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("%w: the pinned host key of %s does not parse: %v", ErrUnreachable, m.Name, err) //nolint:errorlint // one wrapped error is enough
	}
	signer, err := g.cert(m, who, conn)
	if err != nil {
		_ = nc.Close()
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	cfg := &ssh.ClientConfig{User: m.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hk), HostKeyAlgorithms: algorithms(hk)}
	c, chans, reqs, err := ssh.NewClientConn(nc, m.Address, cfg)
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("%w: %s: %v", ErrUnreachable, m.Name, err) //nolint:errorlint // one wrapped error is enough
	}
	_ = nc.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// ---- the SSH listener for agents

func (g *Gate) blocked(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	w := g.fails[ip]
	return w != nil && g.now().Sub(w.start) < failWindow && w.n >= maxFails
}

func (g *Gate) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w := g.fails[ip]
	if w == nil || g.now().Sub(w.start) >= failWindow {
		w = &window{start: g.now()}
		g.fails[ip] = w
	}
	w.n++
	if len(g.fails) > 10000 {
		g.fails = map[string]*window{ip: w}
	}
}

func remoteIP(c net.Conn) string {
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return c.RemoteAddr().String()
	}
	return host
}

// Serve answers SSH connections on ln until it is closed: agents', whose user name is the machine they
// want, and connector machines', which sign in as themselves (connector.go).
func (g *Gate) Serve(ln net.Listener) error {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: g.publicKey,
		VerifiedPublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey, p *ssh.Permissions, _ string) (*ssh.Permissions, error) {
			return p, nil
		},
		ServerVersion: "SSH-2.0-Beacon",
	}
	cfg.AddHostKey(g.d.HostKey)
	for {
		nc, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go g.serveConn(nc, cfg)
	}
}

func (g *Gate) serveConn(nc net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = nc.Close() }()
	ip := remoteIP(nc)
	if g.blocked(ip) {
		return
	}
	_ = nc.SetDeadline(g.now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		var ae *ssh.ServerAuthError
		if errors.As(err, &ae) {
			g.fail(ip)
		}
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer func() { _ = sc.Close() }()
	if name := sc.Permissions.Extensions["connector"]; name != "" {
		g.serveConnector(sc, chans, reqs, name) // a machine's own connection: not an agent's, so not tracked for the pause switch
		return
	}
	who := Actor{Name: sc.Permissions.Extensions["name"], Via: "ssh", Key: sc.Permissions.Extensions["principal"], Cred: sc.Permissions.Extensions["cred"]}
	untrack := g.track(func() { _ = sc.Close() }, who, sc.User())
	defer untrack()

	go func() {
		for r := range reqs { // global requests: keepalives yes, remote forwarding never for agents (a token's key registers no forward)
			if r.WantReply {
				_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	m, merr := g.machineFor(who, sc.User())
	c := &conn{g: g, who: who, machine: m, id: newID(g.now())}
	defer c.close()
	for ch := range chans {
		if merr != nil {
			go refuse(ch, merr)
			continue
		}
		switch ch.ChannelType() {
		case "session":
			go c.session(ch)
		case "direct-tcpip":
			go c.forward(ch)
		default:
			_ = ch.Reject(ssh.UnknownChannelType, "the SSH channel carries sessions and forwarded ports")
		}
	}
}

// refuse answers a channel on a connection that may not reach its machine: sessions get the reason on
// stderr and exit 1, so an agent's ssh prints it; forwarded ports are rejected with it.
func refuse(nc ssh.NewChannel, why error) {
	msg := "beacon: " + why.Error()
	if nc.ChannelType() != "session" {
		_ = nc.Reject(ssh.Prohibited, msg)
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	for r := range reqs {
		switch r.Type {
		case "exec", "shell", "subsystem":
			_ = r.Reply(true, nil)
			_, _ = ch.Stderr().Write([]byte(msg + "\n"))
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
			_ = ch.Close()
			return
		default:
			if r.WantReply {
				_ = r.Reply(r.Type == "pty-req" || r.Type == "env", nil)
			}
		}
	}
}

// conn is one agent connection to one machine, which may carry several channels. The connection to
// the machine is opened with the first channel and shared by the rest.
type conn struct {
	g       *Gate
	who     Actor
	machine store.Machine
	id      string

	mu     sync.Mutex
	client *ssh.Client
	n      int
}

func (c *conn) target() (*ssh.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	cl, err := c.g.dial(context.Background(), c.machine, c.who, c.id)
	if err != nil {
		return nil, err
	}
	c.client = cl
	return cl, nil
}

// next names the next session on this connection.
func (c *conn) next() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return fmt.Sprintf("%s-%d", c.id, c.n)
}

func (c *conn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		_ = c.client.Close()
	}
}
