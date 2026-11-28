package feishu

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
)

// CallbackSkew is how far a signed callback's timestamp may be from Beacon's clock.
const CallbackSkew = 5 * time.Minute

// maxNonces bounds the nonces kept. Only callbacks that verified are kept, and Feishu sends one per
// click; when the table is full of unexpired nonces anyway, new callbacks are refused.
const maxNonces = 10000

// fresh refuses signed callbacks that are stale or replayed: the timestamp must be within CallbackSkew
// of now and the nonce unused in that time. A callback that the SDK then refuses gives its nonce back,
// so a forged request cannot use up a genuine one.
//
// A request without a signature is passed on untouched: with an encrypt key, the SDK accepts no event
// without a valid signature over the timestamp, the nonce, the key and the body, except the URL
// verification challenge, which acts on nothing.
type fresh struct {
	now  func() time.Time
	mu   sync.Mutex
	seen map[string]time.Time // nonce: when it may be forgotten
}

func (f *fresh) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(larkevent.EventSignature) == "" {
			next(w, r)
			return
		}
		nonce := r.Header.Get(larkevent.EventRequestNonce)
		sec, err := strconv.ParseInt(r.Header.Get(larkevent.EventRequestTimestamp), 10, 64)
		now := f.now()
		switch {
		case err != nil || nonce == "" || len(nonce) > 256:
			http.Error(w, "a signed callback needs its timestamp and nonce", http.StatusUnauthorized)
			return
		case time.Unix(sec, 0).Before(now.Add(-CallbackSkew)) || time.Unix(sec, 0).After(now.Add(CallbackSkew)):
			http.Error(w, "the callback's timestamp is too far from now", http.StatusUnauthorized)
			return
		case !f.take(nonce, now):
			http.Error(w, "the callback was seen already", http.StatusUnauthorized)
			return
		}
		rec := &status{ResponseWriter: w, code: http.StatusOK}
		next(rec, r)
		if rec.code != http.StatusOK {
			f.give(nonce)
		}
	}
}

// take reserves nonce, reporting false if it is in use or the table is full.
func (f *fresh) take(nonce string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if until, ok := f.seen[nonce]; ok && now.Before(until) {
		return false
	}
	if len(f.seen) >= maxNonces {
		for n, until := range f.seen {
			if !now.Before(until) {
				delete(f.seen, n)
			}
		}
		if len(f.seen) >= maxNonces {
			return false
		}
	}
	f.seen[nonce] = now.Add(2 * CallbackSkew)
	return true
}

func (f *fresh) give(nonce string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.seen, nonce)
}

// status records the status a handler answers with.
type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
