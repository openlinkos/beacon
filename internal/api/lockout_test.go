package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/clientip"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/testutil"
)

// from is a request from addr, as the lockouts see it.
func from(addr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
	r.RemoteAddr = netip.AddrPortFrom(netip.MustParseAddr(addr), 40000).String()
	return r
}

func lockoutAuth(t *testing.T) (*testutil.Env, *Auth) {
	t.Helper()
	e := testutil.New(t)
	return e, &Auth{Store: e.Store, IP: clientip.New(nil, false)}
}

// TestGenuineRefusalsAreNotFailures checks that a credential Beacon issued, refused because its owner is
// disabled, is not counted as a guess: it never locks its address out.
func TestGenuineRefusalsAreNotFailures(t *testing.T) {
	e, a := lockoutAuth(t)
	agent := people(t, e)
	if _, err := e.Store.UpdateAccount("bob", func(x *store.Account) error { x.Disabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	r := from("192.0.2.10")
	for i := range 3 * maxFails {
		if _, _, err := a.Check(r, agent); !errors.Is(err, ErrInvalid) {
			t.Fatalf("attempt %d of a disabled owner's agent: %v", i, err)
		}
	}
	if a.Blocked(r) {
		t.Fatal("a disabled owner's agent locked its address out")
	}
	if _, _, err := a.Check(r, e.Token(t, "heartbeat-reader", "status:read")); err != nil {
		t.Errorf("another credential from the same address: %v", err)
	}
	// unknown secrets still count
	for range maxFails {
		_, _, _ = a.Check(r, "lbt_unknown")
	}
	if !a.Blocked(r) {
		t.Error("unknown secrets did not lock the address out")
	}
}

// TestLockoutKeepsWorkingCredentials checks that a locked address still has a credential checked that
// worked from it before, while it cannot try any other: a new valid one and an unknown one get the same
// refusal, unread.
func TestLockoutKeepsWorkingCredentials(t *testing.T) {
	e, a := lockoutAuth(t)
	good := e.Token(t, "probe-reader", "status:read")
	fresh := e.Token(t, "fresh", "status:read")
	shared, other := from("192.0.2.20"), from("198.51.100.20")
	if _, _, err := a.Check(shared, good); err != nil {
		t.Fatal(err)
	}
	for i := range maxFails {
		if _, _, err := a.Check(shared, fmt.Sprintf("lbt_guess%d", i)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	if _, _, err := a.Check(shared, good); err != nil {
		t.Errorf("the credential that worked here before: %v", err)
	}
	for _, s := range []string{fresh, "lbt_unknown"} {
		if _, wait, err := a.Check(shared, s); !errors.Is(err, ErrBlocked) || wait <= 0 {
			t.Errorf("a credential new to the locked address got %v (wait %v)", err, wait)
		}
	}
	if _, _, err := a.Check(other, fresh); err != nil {
		t.Errorf("another address: %v", err)
	}
	// a revoked credential is refused even where it worked before
	if err := e.Store.RevokeToken("probe-reader"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Check(shared, good); !errors.Is(err, ErrInvalid) {
		t.Errorf("revoked: %v", err)
	}
}

// TestLockoutCountsIPv6Networks checks that an IPv6 client is counted by its /64, so moving within it
// does not escape the lockout.
func TestLockoutCountsIPv6Networks(t *testing.T) {
	_, a := lockoutAuth(t)
	for i := range maxFails {
		a.Fail(from(fmt.Sprintf("2001:db8:0:1::%x", i+1)))
	}
	if !a.Blocked(from("2001:db8:0:1::ffff")) {
		t.Error("another address in the same /64 is not locked out")
	}
	if a.Blocked(from("2001:db8:0:2::1")) {
		t.Error("the next /64 is locked out")
	}
}

// TestLockoutTablesKeepLocks checks that filling a lockout table makes room without forgetting the
// addresses that are locked out.
func TestLockoutTablesKeepLocks(t *testing.T) {
	_, a := lockoutAuth(t)
	locked := from("192.0.2.30")
	for range maxFails {
		a.Fail(locked)
	}
	for i := range maxEntries + 50 {
		a.Fail(from(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}).String()))
	}
	if !a.Blocked(locked) {
		t.Error("a full table forgot an address that was locked out")
	}
	a.mu.Lock()
	n := len(a.fails.m)
	a.mu.Unlock()
	if n > maxEntries {
		t.Errorf("the table holds %d entries", n)
	}
}

// TestUsernameLockoutSparesOtherAddresses checks that wrong passwords for a username lock it only for
// the addresses that got it wrong: its owner, signing in from where they signed in before, still can.
func TestUsernameLockoutSparesOtherAddresses(t *testing.T) {
	e, a := lockoutAuth(t)
	people(t, e)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	const right = "correct horse battery"
	wrong, own, second := from("198.51.100.40"), from("203.0.113.40"), from("2001:db8:40::1")
	if _, err := a.SignIn(own, "alice", right); err != nil {
		t.Fatal(err)
	}
	for i := range maxUserFails {
		if _, err := a.SignIn(wrong, "alice", fmt.Sprintf("wrong %d", i)); !errors.Is(err, store.ErrBadCredentials) {
			t.Fatalf("wrong password %d: %v", i, err)
		}
	}
	if _, err := a.SignIn(wrong, "alice", right); !errors.Is(err, ErrBlocked) {
		t.Errorf("the address that got the username wrong: %v", err)
	}
	if _, blocked := a.UserBlockedFor(wrong, "alice"); !blocked {
		t.Error("UserBlockedFor does not name the lockout for the address that got it wrong")
	}
	if _, blocked := a.UserBlockedFor(own, "Alice"); blocked {
		t.Error("UserBlockedFor names a lockout for the person's own address")
	}
	if acc, err := a.SignIn(own, "alice", right); err != nil || acc.Username != "alice" {
		t.Fatalf("the person from their own address: %+v %v", acc, err)
	}
	// while the username is locked, another address has only a few tries of its own, each after the
	// username's backoff
	for i := range maxUserAddrFails {
		now = now.Add(userBackoffMax)
		if _, err := a.SignIn(second, "alice", fmt.Sprintf("other %d", i)); !errors.Is(err, store.ErrBadCredentials) {
			t.Fatalf("try %d from a second address: %v", i, err)
		}
	}
	now = now.Add(userBackoffMax)
	if _, err := a.SignIn(second, "alice", right); !errors.Is(err, ErrBlocked) {
		t.Errorf("a second address after its tries: %v", err)
	}
	if _, err := a.SignIn(own, "alice", right); err != nil {
		t.Errorf("the person's own address after the others: %v", err)
	}
}

// TestUsernameBackoffSlowsGuessesFromEverywhere checks that once a username is locked, wrong passwords
// for it from ever new addresses wait longer and longer, whichever address sends them, while the
// person's own address signs in at once; and that the backoff ends when the guessing stops.
func TestUsernameBackoffSlowsGuessesFromEverywhere(t *testing.T) {
	e, a := lockoutAuth(t)
	people(t, e)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	const right = "correct horse battery"
	own := from("203.0.113.60")
	if _, err := a.SignIn(own, "alice", right); err != nil {
		t.Fatal(err)
	}
	next := 0
	fresh := func() *http.Request {
		next++
		return from(netip.AddrFrom4([4]byte{198, 18, byte(next >> 8), byte(next)}).String())
	}
	for i := range maxUserFails {
		if _, err := a.SignIn(fresh(), "alice", fmt.Sprintf("wrong %d", i)); !errors.Is(err, store.ErrBadCredentials) {
			t.Fatalf("wrong password %d: %v", i, err)
		}
	}
	// the username is locked now: each new address waits its turn, and every wrong password doubles it
	wait := userBackoffStart
	for i := range 8 {
		r := fresh()
		if _, err := a.SignIn(r, "alice", "guess"); !errors.Is(err, ErrBlocked) {
			t.Fatalf("guess %d from a new address before its turn: %v", i, err)
		}
		if d, blocked := a.UserBlockedFor(r, "alice"); !blocked || d != wait {
			t.Fatalf("guess %d: the wait is %v (%v), want %v", i, d, blocked, wait)
		}
		if _, err := a.SignIn(own, "alice", right); err != nil {
			t.Fatalf("the person's own address during the backoff: %v", err)
		}
		now = now.Add(wait)
		if _, err := a.SignIn(r, "alice", "guess"); !errors.Is(err, store.ErrBadCredentials) {
			t.Fatalf("guess %d in its turn: %v", i, err)
		}
		wait = min(2*wait, userBackoffMax)
	}
	if wait != userBackoffMax {
		t.Fatalf("the backoff stopped growing at %v", wait)
	}
	// attempts sent together: only the first is checked
	now = now.Add(wait)
	r1, r2 := fresh(), fresh()
	if _, err := a.SignIn(r1, "alice", "together"); !errors.Is(err, store.ErrBadCredentials) {
		t.Fatalf("the first of two at once: %v", err)
	}
	if _, err := a.SignIn(r2, "alice", "together"); !errors.Is(err, ErrBlocked) {
		t.Errorf("the second of two at once: %v", err)
	}
	// the guessing stops: after the window, a new address is not held back
	now = now.Add(failWindow)
	if _, err := a.SignIn(fresh(), "alice", right); err != nil {
		t.Errorf("a new address after the guessing stopped: %v", err)
	}
}

// TestRotatedTokenIsNotLockedOutByItsPredecessor checks that a machine's old token, still retried after
// enroll -rotate replaced it, is refused without counting as a guess: the new token, never used from that
// address yet, still gets in there.
func TestRotatedTokenIsNotLockedOutByItsPredecessor(t *testing.T) {
	e, a := lockoutAuth(t)
	old, _, err := e.Store.CreateSubjectToken("host-edge-1", "edge-1", []string{"host:write"}, 365*24*time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	r := from("192.0.2.50")
	if _, _, err := a.CheckToken(r, old); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.RevokeToken("host-edge-1"); err != nil { // enroll -rotate
		t.Fatal(err)
	}
	fresh, _, err := e.Store.CreateSubjectToken("host-edge-1", "edge-1", []string{"host:write"}, 365*24*time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 * maxFails {
		if _, _, err := a.CheckToken(r, old); !errors.Is(err, ErrInvalid) {
			t.Fatalf("retry %d of the replaced token: %v", i, err)
		}
	}
	if a.Blocked(r) {
		t.Fatal("the replaced token's retries locked its address out")
	}
	if _, _, err := a.CheckToken(r, fresh); err != nil {
		t.Errorf("the new token from the same address: %v", err)
	}
	// on the API's side too
	if _, _, err := a.Check(r, old); !errors.Is(err, ErrInvalid) {
		t.Errorf("the replaced token on the API: %v", err)
	}
}
