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
