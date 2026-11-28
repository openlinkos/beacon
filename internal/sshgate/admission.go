package sshgate

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/store"
)

// What the SSH port lets in before anyone has signed in. Every connection holds a slot until its handshake
// ends, at most handshakeTimeout; an address (an IPv6 address's /64) whose handshakes failed maxFails times
// in failWindow is turned away until the window ends.
const (
	maxFails                = 20
	failWindow              = 10 * time.Minute
	maxFailAddresses        = 10000 // addresses the failure table keeps; the oldest goes first
	maxHandshakes           = 64    // connections still in their handshake, at once
	maxHandshakesPerAddress = 10
	handshakeTimeout        = 30 * time.Second
)

// Refused connections of signed-in identities go to the audit log, at most one per identity, machine and
// reason in refusalEvery, and at most refusalBurst in all in each refusalEvery. The next entry for the
// same identity, machine and reason counts those left out; the log counts the rest.
const (
	refusalEvery = time.Minute
	refusalBurst = 10
	maxRefusals  = 1000 // identity, machine and reason triples remembered
)

type window struct {
	n     int
	start time.Time
}

// admission is the state of the SSH port's limits.
type admission struct {
	mu        sync.Mutex
	fails     map[string]*window
	pending   int
	pendingBy map[string]int

	refused    map[string]*refusal // by identity, machine and reason
	burstStart time.Time
	burst      int
	dropped    int // left out of the audit log this burst, of refusals not remembered
}

// refusal is when a refused identity, machine and reason was last audited and last seen, and how many like
// it were not audited since.
type refusal struct {
	last, seen time.Time
	skipped    int
}

func newAdmission() *admission {
	return &admission{fails: map[string]*window{}, pendingBy: map[string]int{}, refused: map[string]*refusal{}}
}

// addressKey is what the port's limits count by: an IPv4 address, or the /64 of an IPv6 address, which one
// subscriber usually holds whole.
func addressKey(host string) string {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip = ip.Unmap()
	if ip.Is4() {
		return ip.String()
	}
	p, err := ip.WithZone("").Prefix(64)
	if err != nil {
		return host
	}
	return p.String()
}

func (g *Gate) blocked(key string) bool {
	a := g.adm
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.fails[key]
	return w != nil && g.now().Sub(w.start) < failWindow && w.n >= maxFails
}

// fail counts a failed handshake from key. A full table drops the windows that ended, then the oldest.
func (g *Gate) fail(key string) {
	a := g.adm
	now := g.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.fails[key]
	if w == nil || now.Sub(w.start) >= failWindow {
		if w == nil && len(a.fails) >= maxFailAddresses {
			a.evictFails(now)
		}
		w = &window{start: now}
		a.fails[key] = w
	}
	w.n++
}

func (a *admission) evictFails(now time.Time) {
	for k, w := range a.fails {
		if now.Sub(w.start) >= failWindow {
			delete(a.fails, k)
		}
	}
	for len(a.fails) >= maxFailAddresses {
		oldest := ""
		var at time.Time
		for k, w := range a.fails {
			if oldest == "" || w.start.Before(at) {
				oldest, at = k, w.start
			}
		}
		delete(a.fails, oldest)
	}
}

// handshake takes a slot for a connection from key that has not signed in yet; ok is false when the port
// or the address holds as many as it may. release gives the slot back and may be called more than once.
func (g *Gate) handshake(key string) (release func(), ok bool) {
	a := g.adm
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending >= maxHandshakes || a.pendingBy[key] >= maxHandshakesPerAddress {
		return nil, false
	}
	a.pending++
	a.pendingBy[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.pending--
			if a.pendingBy[key]--; a.pendingBy[key] <= 0 {
				delete(a.pendingBy, key)
			}
		})
	}, true
}

// turnedAway counts a connection closed before its handshake: the address was blocked, or the port busy.
func (g *Gate) turnedAway(reason string) {
	if g.d.Metrics != nil {
		g.d.Metrics.Inc("beacon_ssh_connections_refused_total", "Connections to the SSH channel closed before their handshake, by reason",
			metrics.Labels("reason", reason))
	}
}

// readConn counts what the client sent, to tell a failed handshake from a connection closed before it said
// anything (a port check).
type readConn struct {
	net.Conn
	n atomic.Int64
}

func (c *readConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// failedHandshake reports whether a handshake that ended in err counts against its address: every one
// does, except a connection the client closed before sending a byte.
func failedHandshake(c *readConn, err error) bool {
	if c.n.Load() > 0 {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true // it held a slot until the deadline
	}
	return !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed)
}

// refusalReason names why a connection was refused, in words of Beacon's own, never the client's.
func refusalReason(err error) string {
	switch {
	case errors.Is(err, ErrPaused):
		return "paused"
	case errors.Is(err, ErrStandby):
		return "standby"
	case errors.Is(err, ErrNoMachine):
		return "no such machine"
	case errors.Is(err, ErrMachineChanged):
		return "machine changed"
	case errors.Is(err, ErrEvidenceUnavailable):
		return "evidence unavailable"
	case errors.Is(err, ErrForbidden):
		return "not permitted"
	}
	return "refused"
}

// refuseAudit records that who was refused machine, unless as much was recorded within refusalEvery or
// the burst is spent. The machine name is the client's, so it is kept only when it is a machine name.
func (g *Gate) refuseAudit(who Actor, machine string, err error) {
	reason := refusalReason(err)
	if !store.MachineName.MatchString(machine) {
		machine, reason = "", reason+" (not a machine name)"
	}
	a := g.adm
	now := g.now()
	key := who.Key + "\x00" + who.Cred + "\x00" + machine + "\x00" + reason
	a.mu.Lock()
	if now.Sub(a.burstStart) >= refusalEvery {
		if a.dropped > 0 {
			g.d.Log.Warn("refused SSH connections were left out of the audit log", "count", a.dropped)
		}
		a.burstStart, a.burst, a.dropped = now, 0, 0
	}
	r := a.refused[key]
	if r == nil {
		if len(a.refused) >= maxRefusals {
			a.forgetRefusals(now)
		}
		if len(a.refused) >= maxRefusals {
			a.dropped++
			a.mu.Unlock()
			return
		}
		r = &refusal{}
		a.refused[key] = r
	}
	r.seen = now
	if now.Sub(r.last) < refusalEvery || a.burst >= refusalBurst {
		r.skipped++
		a.mu.Unlock()
		return
	}
	detail := reason
	if r.skipped > 0 {
		detail = fmt.Sprintf("%s; %d more not recorded", reason, r.skipped)
	}
	r.last, r.skipped = now, 0
	a.burst++
	a.mu.Unlock()
	if err := g.d.Store.Audit(store.AuditEntry{Actor: who.Name, Via: "ssh", Action: "ssh_refused", Target: machine, Detail: detail}); err != nil {
		g.d.Log.Warn("refused SSH connection was not audited", "err", err)
	}
}

// forgetRefusals makes room among the refusals remembered: those not seen for refusalEvery go, and what
// they left out is counted for the log. a.mu is held.
func (a *admission) forgetRefusals(now time.Time) {
	for k, r := range a.refused {
		if now.Sub(r.seen) >= refusalEvery {
			a.dropped += r.skipped
			delete(a.refused, k)
		}
	}
}
