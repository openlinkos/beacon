package gateway

import (
	"sync"
	"time"
)

// Change events are limited per token (changes.go): one token may post changeLimit of them in a
// changeWindow, and the ones beyond are refused with 429 and changes.too_many until the window is over,
// so that one runaway sender cannot fill the store or hide other senders' events.
const (
	changeLimit  = 60
	changeWindow = time.Minute
)

// Alert posts from tokens with alerts:send are limited the same way: alertLimit posts in an alertWindow,
// those beyond refused with 429 and alerts.too_many. A rule engine posts once per rule group and
// evaluation. One post takes at most alertsPerPost alerts from such a token; the rest are dropped and
// counted. The router holds a token to a number of firing alerts and groups too (router.SenderAlerts,
// router.SenderGroups). The rule engine's own posts, with the rules token, are never limited.
const (
	alertLimit    = 120
	alertWindow   = time.Minute
	alertsPerPost = 200
)

// limiter counts requests by key in fixed windows.
type limiter struct {
	mu      sync.Mutex
	windows map[string]*count
}

type count struct {
	n     int
	start time.Time
}

// allow counts one request by key at now and reports whether it is within limit for the window of every.
func (l *limiter) allow(key string, now time.Time, limit int, every time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = map[string]*count{}
	}
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= every {
		w = &count{start: now}
		l.windows[key] = w
	}
	w.n++
	if len(l.windows) > 10000 { // forget everyone rather than grow without bound
		l.windows = map[string]*count{key: w}
	}
	return w.n <= limit
}
