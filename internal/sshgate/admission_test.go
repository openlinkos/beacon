package sshgate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/store"
)

// TestSubsystemKindsAreFixed: a subsystem's name, which the client chooses, is kept as its command only;
// the kind, the audit action and the metric label come from a fixed list.
func TestSubsystemKindsAreFixed(t *testing.T) {
	needShell(t)
	e := setup(t)
	m := metrics.New("test")
	e.g.d.Metrics = m
	cl, err := e.client(t, "box", e.agentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestSubsystem("pause"); err == nil {
		t.Fatal("the test machine has no such subsystem")
	}
	_ = cl.Close() // the test machine keeps the channel open after refusing: end it with the connection
	recs := waitSessions(t, e.st, 1)
	if len(recs) != 1 || recs[0].Kind != "subsystem" || recs[0].Command != "pause" {
		t.Fatalf("record %+v", recs)
	}
	waitFor(t, "the audit entry", func() bool { return audited(e.st, "ssh_subsystem_refused", "box", "pause") })
	for _, a := range e.st.AuditLog(20) {
		if a.Action == "ssh_pause" {
			t.Fatalf("a client named an audit action: %+v", a)
		}
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, nil)
	if body := w.Body.String(); !strings.Contains(body, `beacon_ssh_sessions_total{kind="subsystem",via="ssh"} 1`) || strings.Contains(body, `kind="pause"`) {
		t.Fatalf("metrics:\n%s", body)
	}
	if got := subsystemKind("sftp"); got != "sftp" {
		t.Fatalf("sftp is %q", got)
	}
}

// TestRefusedConnectionsAreAudited: an identity turned away from a machine is on record, once a minute
// for the same machine and reason; a name that is no machine's is not repeated.
func TestRefusedConnectionsAreAudited(t *testing.T) {
	needShell(t)
	e := setup(t)
	if _, err := e.st.UpdateToken("claude-code", func(tk *store.Token) error { tk.MachineAccess = store.NamedAccess("other"); return nil }); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, _, code := e.run(t, "box", "true"); code != 1 {
			t.Fatalf("exit %d", code)
		}
	}
	e.run(t, "db", "true")
	e.run(t, "Not_A_Machine", "true")
	if _, err := e.st.UpdateToken("claude-code", func(tk *store.Token) error { tk.MachineAccess = store.AllAccess(); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.g.SetPaused(context.Background(), Actor{Name: "root", Via: "cli"}, true); err != nil {
		t.Fatal(err)
	}
	e.run(t, "box", "true")
	waitFor(t, "the audit entries", func() bool { return audited(e.st, "ssh_refused", "box", "paused") })
	count := map[string]int{}
	for _, a := range e.st.AuditLog(50) {
		if a.Action != "ssh_refused" {
			continue
		}
		if a.Actor != "claude-code" || a.Via != "ssh" || strings.Contains(a.Target+a.Detail, "Not_A_Machine") {
			t.Errorf("entry %+v", a)
		}
		count[a.Target+" "+a.Detail]++
	}
	want := map[string]int{"box not permitted": 1, "db no such machine": 1, " no such machine (not a machine name)": 1, "box paused": 1}
	if fmt.Sprint(count) != fmt.Sprint(want) {
		t.Fatalf("refusals on record: %v, want %v", count, want)
	}
	// a minute on, the next refusal of box for the same reason counts the two left out
	e.g.adm.mu.Lock()
	for _, r := range e.g.adm.refused {
		r.last = r.last.Add(-refusalEvery)
	}
	e.g.adm.mu.Unlock()
	if err := e.g.SetPaused(context.Background(), Actor{Name: "root", Via: "cli"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UpdateToken("claude-code", func(tk *store.Token) error { tk.MachineAccess = store.NamedAccess("other"); return nil }); err != nil {
		t.Fatal(err)
	}
	e.run(t, "box", "true")
	waitFor(t, "the next entry", func() bool { return audited(e.st, "ssh_refused", "box", "not permitted; 2 more not recorded") })
}

// TestRefusalAuditIsLimited: refusals cannot flood the audit log; those left out are counted into the next
// entry for the same identity, machine and reason.
func TestRefusalAuditIsLimited(t *testing.T) {
	g, st, clock := unitGate(t)
	who := Actor{Name: "claude-code", Via: "ssh", Key: "token:claude-code"}
	for i := range 25 {
		g.refuseAudit(who, fmt.Sprintf("m%d", i), ErrForbidden)
	}
	g.refuseAudit(who, "m0", ErrForbidden)
	g.refuseAudit(who, "m0", ErrForbidden)
	if n := countAudit(st, "ssh_refused"); n != refusalBurst {
		t.Fatalf("%d entries in the first minute", n)
	}
	clock.Add(refusalEvery)
	g.refuseAudit(who, "m0", ErrForbidden)
	g.refuseAudit(who, "m24", ErrForbidden)
	a := st.AuditLog(2)
	if len(a) != 2 || a[1].Target != "m0" || a[1].Detail != "not permitted; 2 more not recorded" ||
		a[0].Target != "m24" || a[0].Detail != "not permitted; 1 more not recorded" {
		t.Fatalf("latest %+v", a)
	}
	// a full table forgets what it has not seen for a minute
	for i := range maxRefusals {
		g.refuseAudit(who, fmt.Sprintf("n%d", i), ErrForbidden)
	}
	clock.Add(refusalEvery)
	before := countAudit(st, "ssh_refused")
	g.refuseAudit(who, "late", ErrForbidden)
	if countAudit(st, "ssh_refused") != before+1 {
		t.Fatal("a full table of refusals kept out a new one")
	}
	g.adm.mu.Lock()
	n := len(g.adm.refused)
	g.adm.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d refusals remembered", n)
	}
}

// TestHandshakeSlots: the SSH port holds only so many connections that have not signed in, in all and
// from one address; a slot comes back when its handshake ends.
func TestHandshakeSlots(t *testing.T) {
	g, _, _ := unitGate(t)
	var releases []func()
	for i := range maxHandshakes {
		release, ok := g.handshake(fmt.Sprintf("192.0.2.%d", i%200))
		if !ok {
			t.Fatalf("slot %d refused", i)
		}
		releases = append(releases, release)
	}
	if _, ok := g.handshake("198.51.100.1"); ok {
		t.Fatal("the port took more handshakes than its limit")
	}
	releases[0]()
	releases[0]() // twice is once
	release, ok := g.handshake("198.51.100.1")
	if !ok {
		t.Fatal("a released slot did not come back")
	}
	if _, ok := g.handshake("198.51.100.2"); ok {
		t.Fatal("a double release gave back two slots")
	}
	release()
	for _, r := range releases {
		r()
	}
	for i := range maxHandshakesPerAddress {
		if _, ok := g.handshake("203.0.113.7"); !ok {
			t.Fatalf("slot %d of one address refused", i)
		}
	}
	if _, ok := g.handshake("203.0.113.7"); ok {
		t.Fatal("one address took more handshakes than its limit")
	}
	if _, ok := g.handshake("203.0.113.8"); !ok {
		t.Fatal("another address was refused")
	}
}

// TestHandshakeSlotsOverTheNetwork: connections that do not sign in hold the address's slots; the next one
// is closed before the handshake, until one of them ends.
func TestHandshakeSlotsOverTheNetwork(t *testing.T) {
	e := setup(t)
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range maxHandshakesPerAddress {
		c, ok := banner(t, e.addr)
		if !ok {
			t.Fatal("a connection within the limit got no banner")
		}
		conns = append(conns, c)
	}
	if c, ok := banner(t, e.addr); ok {
		_ = c.Close()
		t.Fatal("a connection past the limit was answered")
	}
	_ = conns[0].Close()
	waitFor(t, "a slot", func() bool {
		c, ok := banner(t, e.addr)
		if ok {
			conns = append(conns, c)
		}
		return ok
	})
}

// TestFailedHandshakesCount: every failed handshake counts against its address, not only a refused key;
// a connection closed before it said anything (a port check) does not.
func TestFailedHandshakesCount(t *testing.T) {
	e := setup(t)
	for range maxFails + 5 { // port checks
		c, ok := banner(t, e.addr)
		if !ok {
			t.Fatal("no banner")
		}
		_ = c.Close()
	}
	waitFor(t, "the handshakes to end", func() bool {
		e.g.adm.mu.Lock()
		defer e.g.adm.mu.Unlock()
		return e.g.adm.pending == 0
	})
	if e.g.blocked(addressKey("127.0.0.1")) {
		t.Fatal("port checks blocked the address")
	}
	for range maxFails {
		c, ok := banner(t, e.addr)
		if !ok {
			t.Fatal("no banner")
		}
		_, _ = c.Write([]byte("this is not SSH\r\n"))
		_ = c.Close()
	}
	waitFor(t, "the address to be blocked", func() bool { return e.g.blocked(addressKey("127.0.0.1")) })
	if c, ok := banner(t, e.addr); ok {
		_ = c.Close()
		t.Fatal("a blocked address was answered")
	}
}

// TestRefusedKeySignInIsAudited: a key whose credential may not sign in is on record once its holder has
// signed with it, with the limits of every refused connection; offering the public key alone is not.
func TestRefusedKeySignInIsAudited(t *testing.T) {
	e := setup(t)
	if _, _, err := e.st.CreateToken("dashboards", []string{"status:read"}, 24*time.Hour, "test"); err != nil {
		t.Fatal(err)
	}
	readOnly := signer(t)
	e.giveKey(t, "dashboards", readOnly)
	for range 2 {
		if cl, err := e.client(t, "box", readOnly); err == nil {
			_ = cl.Close()
			t.Fatal("a key without machines:ssh signed in")
		}
	}
	waitFor(t, "the audit entry", func() bool { return audited(e.st, "ssh_refused", "box", refusedNoSSH) })

	e.st.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) }) // past the token's expiry
	if cl, err := e.client(t, "box", e.agentKey); err == nil {
		_ = cl.Close()
		t.Fatal("an expired token's key signed in")
	}
	waitFor(t, "the audit entry", func() bool { return audited(e.st, "ssh_refused", "box", refusedInactive) })
	n := countAudit(e.st, "ssh_refused")

	if cl, err := e.client(t, "box", unsigned{e.agentKey}); err == nil { // the public key, without the private one
		_ = cl.Close()
		t.Fatal("a key that never signed signed in")
	}
	time.Sleep(200 * time.Millisecond)
	if got := countAudit(e.st, "ssh_refused"); got != n {
		t.Fatalf("offering a public key alone was audited: %d entries, then %d", n, got)
	}
	for _, a := range e.st.AuditLog(100) {
		if a.Action == "ssh_refused" && strings.Contains(a.Detail, refusedNoSSH) && a.Actor != "dashboards" {
			t.Errorf("audited as %q", a.Actor)
		}
	}
	if got := countAudit(e.st, "ssh_refused"); got != 2 {
		t.Fatalf("%d refusals audited, want one per credential and reason", got)
	}
}

// unsigned offers a key it cannot sign with, as a client holding only someone's public key.
type unsigned struct{ ssh.Signer }

func (unsigned) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	return nil, errors.New("no private key")
}

// TestFailureTableEvictsTheOldest: a full table of failures makes room by its oldest entry, so an address
// blocked recently stays blocked.
func TestFailureTableEvictsTheOldest(t *testing.T) {
	g, _, clock := unitGate(t)
	for i := range maxFailAddresses - 1 {
		g.fail(fmt.Sprintf("a%d", i))
		clock.Add(time.Millisecond / 100)
	}
	clock.Add(time.Second)
	for range maxFails {
		g.fail("blocked")
	}
	clock.Add(time.Second)
	g.fail("newcomer")
	if !g.blocked("blocked") {
		t.Fatal("a full table forgot a blocked address")
	}
	g.adm.mu.Lock()
	n, oldest, newest := len(g.adm.fails), g.adm.fails["a0"], g.adm.fails["newcomer"]
	g.adm.mu.Unlock()
	if n != maxFailAddresses || oldest != nil || newest == nil {
		t.Fatalf("%d entries, oldest kept %v, newest %v", n, oldest != nil, newest != nil)
	}
}

func TestAddressKey(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.1":            "192.0.2.1",
		"::ffff:192.0.2.1":     "192.0.2.1",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2::9":      "2001:db8:1:2::/64",
		"fe80::1%en0":          "fe80::/64",
		"pipe":                 "pipe",
	} {
		if got := addressKey(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// ---- helpers

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// unitGate is a channel that serves nothing, on a clock of the test's own.
func unitGate(t *testing.T) (*Gate, *store.Store, *testClock) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := &testClock{t: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	g, err := New(Deps{Store: st, HostKey: signer(t), CA: signer(t), Dir: t.TempDir(), Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return g, st, clock
}

// banner connects to addr and reports whether the channel answered with its version.
func banner(t *testing.T, addr string) (net.Conn, bool) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	_ = c.SetReadDeadline(time.Time{})
	if err != nil || !strings.HasPrefix(line, "SSH-2.0-Beacon") {
		_ = c.Close()
		return nil, false
	}
	return c, true
}

func audited(st *store.Store, action, target, detail string) bool {
	for _, a := range st.AuditLog(100) {
		if a.Action == action && a.Target == target && strings.Contains(a.Detail, detail) {
			return true
		}
	}
	return false
}

func countAudit(st *store.Store, action string) int {
	n := 0
	for _, a := range st.AuditLog(1000) {
		if a.Action == action {
			n++
		}
	}
	return n
}
