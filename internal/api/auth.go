package api

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/clientip"
	"github.com/openlinkos/beacon/internal/store"
)

// Auth checks bearer tokens and people's passwords, and limits failed attempts per client address and,
// for passwords, per username.
//
// An address is an IPv4 address, or an IPv6 address's /64, which one machine or one household usually
// holds whole. Only a secret that Beacon did not issue counts as a failure: a genuine credential refused
// for what it may do, or because its owner is disabled, is not a guess. After maxFails failures in
// failWindow the address is locked out until the window ends. A secret is then refused unread unless it
// was accepted from the same address earlier (within goodFor), so a client with a working credential
// keeps working beside one that keeps failing, while the locked address cannot be used to try new
// secrets.
//
// A username is locked after maxUserFails wrong passwords in failWindow, from anywhere. While it is, an
// address that has itself got that username wrong maxUserAddrFails times is refused for it; an address
// that has not, such as the person's own, may still sign in, and each wrong password from it counts.
// Once it is locked, the username is also slowed down as a whole (backoff), so that guessing it from
// many addresses gets no faster than from one; an address the person signed in from within goodFor is
// not slowed down.
type Auth struct {
	Store *store.Store
	IP    *clientip.Resolver
	Now   func() time.Time

	mu            sync.Mutex
	fails         windows[netip.Prefix]
	userFails     windows[string]
	userAddrFails windows[userAddr]
	userBackoff   map[string]*backoff
	good          seen[goodKey]  // secrets accepted from an address
	userGood      seen[userAddr] // usernames signed in to from an address
}

// backoff slows down sign-ins to one username, from every address but those the person signed in from:
// once the username is locked, each wrong password for it makes the next attempt wait longer, from
// userBackoffStart doubling up to userBackoffMax. It ends failWindow after the last wrong password.
type backoff struct {
	n     int       // wrong passwords since it began
	last  time.Time // the last of them
	until time.Time // no attempt before this
}

type window struct {
	n     int
	start time.Time
}

const (
	maxFails   = 20
	failWindow = 10 * time.Minute
	// maxUserFails is how many wrong passwords one username takes in failWindow, from anywhere, before
	// it is locked.
	maxUserFails = 10
	// maxUserAddrFails is how many wrong passwords for a locked username one address may have made in
	// failWindow before it is refused for that username.
	maxUserAddrFails = 3
	// goodFor is how long a secret accepted from an address still passes that address's lockout, and
	// how long a person's sign-in from an address spares it the username's backoff.
	goodFor = 7 * 24 * time.Hour
	// userBackoffStart is the first wait of a locked username's backoff, and userBackoffMax the longest.
	userBackoffStart = time.Second
	userBackoffMax   = time.Minute
	// maxEntries bounds each table the lockouts keep. When one is full, an entry whose window has ended
	// makes room, else the oldest that has not locked anything out, and only then the oldest: so a
	// flood of new addresses or names cannot wipe out the counts that are locking others.
	maxEntries = 10000
)

// ErrBlocked means too many sign-ins failed recently, from this address or for this username.
var ErrBlocked = errors.New("too many failed sign-ins; try again in a few minutes")

// ErrInvalid means a secret is unknown, has expired or was revoked, or its holder may not act now.
var ErrInvalid = errors.New("the credential is unknown or has expired")

type userAddr struct {
	user string
	addr netip.Prefix
}

type goodKey struct {
	addr netip.Prefix
	hash [sha256.Size]byte
}

// windows counts failures by key, each in a window of failWindow from its first failure.
type windows[K comparable] struct{ m map[K]*window }

func (t *windows[K]) get(k K) *window { return t.m[k] }

// count adds a failure for k; limit is the count at which a window locks out.
func (t *windows[K]) count(k K, now time.Time, limit int) {
	if t.m == nil {
		t.m = map[K]*window{}
	}
	w := t.m[k]
	if w == nil || now.Sub(w.start) >= failWindow {
		if w == nil && len(t.m) >= maxEntries {
			t.makeRoom(now, limit)
		}
		w = &window{start: now}
		t.m[k] = w
	}
	w.n++
}

// makeRoom forgets the windows that have ended and, if that frees nothing, the oldest one that has
// not locked out, or failing that the oldest of all.
func (t *windows[K]) makeRoom(now time.Time, limit int) {
	var open, locked K
	var openAt, lockedAt time.Time
	hasOpen, hasLocked := false, false
	for k, w := range t.m {
		switch {
		case now.Sub(w.start) >= failWindow:
			delete(t.m, k)
		case w.n < limit:
			if !hasOpen || w.start.Before(openAt) {
				open, openAt, hasOpen = k, w.start, true
			}
		default:
			if !hasLocked || w.start.Before(lockedAt) {
				locked, lockedAt, hasLocked = k, w.start, true
			}
		}
	}
	switch {
	case len(t.m) < maxEntries:
	case hasOpen:
		delete(t.m, open)
	case hasLocked:
		delete(t.m, locked)
	}
}

// addrKey is what failures are counted against for a client address.
func addrKey(a netip.Addr) netip.Prefix {
	a = a.Unmap()
	if !a.IsValid() {
		return netip.Prefix{}
	}
	bits := 32
	if a.Is6() {
		bits = 64
	}
	p, _ := a.Prefix(bits)
	return p
}

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Auth) client(r *http.Request) netip.Addr {
	if a.IP != nil {
		ip, _ := a.IP.ClientIP(r)
		return ip
	}
	return netip.Addr{}
}

// Separate is an Auth on the same store and client addresses that counts its failures apart. The
// machines' gateway keeps its own, so that what people's and agents' clients get wrong never stops
// monitoring data, and the other way round.
func (a *Auth) Separate() *Auth { return &Auth{Store: a.Store, IP: a.IP, Now: a.Now} }

// BlockedFor reports how much of the address's lockout is left, and whether there is one: how long
// the client must wait before trying again with anything but a credential that already worked from
// here.
func (a *Auth) BlockedFor(r *http.Request) (time.Duration, bool) {
	addr := addrKey(a.client(r))
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.left(a.fails.get(addr), maxFails)
}

// BlockedForSecret is BlockedFor for a request that presents secret: a secret that worked from the
// address, or that Beacon issued to a client there (Remember), passes the lockout.
func (a *Auth) BlockedForSecret(r *http.Request, secret string) (time.Duration, bool) {
	addr := addrKey(a.client(r))
	a.mu.Lock()
	defer a.mu.Unlock()
	wait, blocked := a.left(a.fails.get(addr), maxFails)
	if blocked && secret != "" && a.worked(goodKey{addr: addr, hash: sha256.Sum256([]byte(secret))}) {
		return 0, false
	}
	return wait, blocked
}

// Remember notes secrets Beacon has just issued to the request's client as having worked from its
// address: an OAuth client's new access and refresh tokens, which replace the ones it signed in with, so
// that it keeps working there during a lockout as a token that worked there does.
func (a *Auth) Remember(r *http.Request, secrets ...string) {
	addr := addrKey(a.client(r))
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range secrets {
		if s != "" {
			a.remember(goodKey{addr: addr, hash: sha256.Sum256([]byte(s))})
		}
	}
}

// Blocked reports whether the client's address is locked out.
func (a *Auth) Blocked(r *http.Request) bool {
	_, blocked := a.BlockedFor(r)
	return blocked
}

// left is the time until the window that locked out with limit failures ends.
func (a *Auth) left(w *window, limit int) (time.Duration, bool) {
	if w == nil {
		return 0, false
	}
	if left := failWindow - a.now().Sub(w.start); w.n >= limit && left > 0 {
		return left, true
	}
	return 0, false
}

// WaitWords says a lockout's wait as a person reads it: "a minute", "9 minutes". Errors carry it so
// a client knows how long to wait before trying again.
func WaitWords(d time.Duration) string {
	minutes := int((d + time.Minute - 1) / time.Minute)
	if minutes <= 1 {
		return "a minute"
	}
	return strconv.Itoa(minutes) + " minutes"
}

// RetryAfter is a lockout's wait in whole seconds, rounded up, for the Retry-After header.
// The gateway answers its lockouts the same way the API does, so it shares these words.
func RetryAfter(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}

// Fail counts a failed attempt from the request's client: a wrong code or refresh token on the OAuth
// token endpoint, say.
func (a *Auth) Fail(r *http.Request) {
	addr := addrKey(a.client(r))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fails.count(addr, a.now(), maxFails)
}

// Client is the request's client address, as the configured proxies give it.
func (a *Auth) Client(r *http.Request) netip.Addr { return a.client(r) }

// Attempt checks a secret under the address's lockout. verify does the checking and reports whether
// the secret is accepted, and whether a refused secret is nonetheless genuine (one Beacon issued),
// which does not count as a failure. While the address is locked out, verify runs only for a secret
// accepted from this address before; any other gets ErrBlocked and the wait, unread. A refused secret
// gets ErrInvalid.
func (a *Auth) Attempt(r *http.Request, secret string, verify func() (accepted, genuine bool)) (time.Duration, error) {
	addr := addrKey(a.client(r))
	key := goodKey{addr: addr, hash: sha256.Sum256([]byte(secret))}
	a.mu.Lock()
	if wait, blocked := a.left(a.fails.get(addr), maxFails); blocked && !a.worked(key) {
		a.mu.Unlock()
		return wait, ErrBlocked
	}
	a.mu.Unlock()
	accepted, genuine := verify()
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case accepted:
		a.remember(key)
		return 0, nil
	case !genuine:
		a.fails.count(addr, a.now(), maxFails)
	}
	return 0, ErrInvalid
}

// worked reports whether the secret was accepted from the address within goodFor. The caller holds mu.
func (a *Auth) worked(k goodKey) bool { return a.good.recent(k, a.now()) }

// remember notes that the secret was accepted from the address. The caller holds mu.
func (a *Auth) remember(k goodKey) { a.good.note(k, a.now()) }

// seen keeps when each key was last seen, for goodFor, in at most maxEntries entries: when it is full,
// those older than goodFor go, and then the oldest.
type seen[K comparable] struct{ m map[K]time.Time }

func (s *seen[K]) recent(k K, now time.Time) bool {
	at, ok := s.m[k]
	return ok && now.Sub(at) < goodFor
}

func (s *seen[K]) note(k K, now time.Time) {
	if s.m == nil {
		s.m = map[K]time.Time{}
	}
	if _, ok := s.m[k]; !ok && len(s.m) >= maxEntries {
		var oldest K
		var at time.Time
		found := false
		for g, t := range s.m {
			if now.Sub(t) >= goodFor {
				delete(s.m, g)
				continue
			}
			if !found || t.Before(at) {
				oldest, at, found = g, t, true
			}
		}
		if len(s.m) >= maxEntries && found {
			delete(s.m, oldest)
		}
	}
	s.m[k] = now
}

// CheckToken checks a token secret for the request, counting failures: the machines' check, on the
// gateway, where an agent's token carries no scopes of its own. It reports ErrBlocked with the wait,
// or ErrInvalid.
func (a *Auth) CheckToken(r *http.Request, secret string) (store.Token, time.Duration, error) {
	var t store.Token
	wait, err := a.Attempt(r, secret, func() (bool, bool) {
		var ok bool
		t, ok = a.Store.VerifyToken(secret)
		return ok, !ok && a.Store.Issued(secret)
	})
	if err != nil {
		return store.Token{}, wait, err
	}
	return t, 0, nil
}

// Verify is CheckToken without the reason for a refusal.
func (a *Auth) Verify(r *http.Request, secret string) (store.Token, bool) {
	t, _, err := a.CheckToken(r, secret)
	return t, err == nil
}

// Check checks a bearer secret for the API, MCP and the pages, counting failures: a token or an OAuth
// access token, resolved to who it is and what it may do now. It reports ErrBlocked with the wait, or
// ErrInvalid.
func (a *Auth) Check(r *http.Request, secret string) (store.Principal, time.Duration, error) {
	var p store.Principal
	wait, err := a.Attempt(r, secret, func() (bool, bool) {
		var ok bool
		p, ok = a.Store.Authenticate(secret)
		return ok, !ok && a.Store.Issued(secret)
	})
	if err != nil {
		return store.Principal{}, wait, err
	}
	return p, 0, nil
}

// Authenticate is Check without the reason for a refusal.
func (a *Auth) Authenticate(r *http.Request, secret string) (store.Principal, bool) {
	p, _, err := a.Check(r, secret)
	return p, err == nil
}

// UserBlockedFor is BlockedFor for one username's wrong passwords, as they apply to the request's
// client: a locked username stays open to an address that has not got it wrong itself, once its
// backoff allows the next attempt (at once from an address the person signed in from).
func (a *Auth) UserBlockedFor(r *http.Request, username string) (time.Duration, bool) {
	addr := addrKey(a.client(r))
	username = normalUser(username)
	a.mu.Lock()
	defer a.mu.Unlock()
	wait, locked := a.userLocked(username, addr)
	if slow, slowed := a.slowed(username, addr, a.now()); slowed && slow > wait {
		wait, locked = slow, true
	}
	return wait, locked
}

// slowed is how long the username's backoff keeps addr waiting, if it does. The caller holds mu.
func (a *Auth) slowed(username string, addr netip.Prefix, now time.Time) (time.Duration, bool) {
	b := a.userBackoff[username]
	if b == nil || !now.Before(b.until) || a.userGood.recent(userAddr{user: username, addr: addr}, now) {
		return 0, false
	}
	return b.until.Sub(now), true
}

// active is the username's backoff while it lasts. The caller holds mu.
func (a *Auth) active(username string, now time.Time) *backoff {
	b := a.userBackoff[username]
	if b == nil || now.Sub(b.last) >= failWindow {
		return nil
	}
	return b
}

// backoffDelay is the wait after the nth wrong password of a backoff.
func backoffDelay(n int) time.Duration {
	d := userBackoffStart
	for i := 1; i < n && d < userBackoffMax; i++ {
		d *= 2
	}
	return min(d, userBackoffMax)
}

// slowDown counts a wrong password for username in its backoff, which begins once the username is
// locked. The caller holds mu.
func (a *Auth) slowDown(username string, now time.Time) {
	b := a.active(username, now)
	if b == nil {
		if _, locked := a.left(a.userFails.get(username), maxUserFails); !locked {
			return
		}
		if a.userBackoff == nil {
			a.userBackoff = map[string]*backoff{}
		}
		if len(a.userBackoff) >= maxEntries { // those that have ended go first, then the one idle longest
			var idle string
			var at time.Time
			for u, x := range a.userBackoff {
				if now.Sub(x.last) >= failWindow {
					delete(a.userBackoff, u)
				} else if idle == "" || x.last.Before(at) {
					idle, at = u, x.last
				}
			}
			if len(a.userBackoff) >= maxEntries {
				delete(a.userBackoff, idle)
			}
		}
		b = &backoff{}
		a.userBackoff[username] = b
	}
	b.n++
	b.last, b.until = now, now.Add(backoffDelay(b.n))
}

// userLocked is how long username stays refused to addr. The caller holds mu.
func (a *Auth) userLocked(username string, addr netip.Prefix) (time.Duration, bool) {
	all, locked := a.left(a.userFails.get(username), maxUserFails)
	if !locked {
		return 0, false
	}
	own, locked := a.left(a.userAddrFails.get(userAddr{user: username, addr: addr}), maxUserAddrFails)
	if !locked {
		return 0, false
	}
	return min(all, own), true
}

func normalUser(username string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 64 {
		username = username[:64]
	}
	return username
}

// SignIn checks a person's username and password, limited per address and per username:
// ErrBlocked after too many failures, store.ErrBadCredentials for a wrong username or password.
//
// While the username's backoff lasts, an attempt from an address the person has not signed in from
// takes its turn before the password is checked: the next such attempt waits as long again, so that
// attempts sent at once do not all pass together.
func (a *Auth) SignIn(r *http.Request, username, password string) (store.Account, error) {
	username = normalUser(username)
	addr := addrKey(a.client(r))
	a.mu.Lock()
	now := a.now()
	_, blocked := a.left(a.fails.get(addr), maxFails)
	if !blocked {
		_, blocked = a.userLocked(username, addr)
	}
	if !blocked {
		_, blocked = a.slowed(username, addr, now)
	}
	if b := a.active(username, now); !blocked && b != nil && !a.userGood.recent(userAddr{user: username, addr: addr}, now) {
		b.until = now.Add(backoffDelay(b.n))
	}
	a.mu.Unlock()
	if blocked {
		return store.Account{}, ErrBlocked
	}
	acc, err := a.Store.SignIn(username, password)
	a.mu.Lock()
	defer a.mu.Unlock()
	now = a.now()
	switch {
	case err == nil:
		a.userGood.note(userAddr{user: username, addr: addr}, now)
	case errors.Is(err, store.ErrBadCredentials):
		a.fails.count(addr, now, maxFails)
		a.userFails.count(username, now, maxUserFails)
		a.userAddrFails.count(userAddr{user: username, addr: addr}, now, maxUserAddrFails)
		a.slowDown(username, now)
	}
	return acc, err
}
