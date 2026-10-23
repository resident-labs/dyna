// Package ratelimit counts requests per key in fixed time windows.
package ratelimit

import (
	"sync"
	"time"
)

// maxKeys bounds the key map before stale windows are pruned.
const maxKeys = 4096

// Limiter counts requests per key inside fixed windows. The clock is injected
// so callers can test a window rollover without waiting.
type Limiter struct {
	mu   sync.Mutex
	now  func() time.Time
	seen map[string]window
}

// window is the start of a counted window and the requests seen in it.
type window struct {
	start int64
	count int64
}

// New makes a limiter that reads the time from now. A nil now reads time.Now.
func New(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, seen: map[string]window{}}
}

// Allow reports whether key is within max requests in a window of length per,
// and counts the call. Each key counts in its own window.
func (l *Limiter) Allow(key string, max int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := l.now().Truncate(per).Unix()
	w := l.seen[key]
	if w.start != start {
		w = window{start: start}
	}
	w.count++
	l.seen[key] = w
	if len(l.seen) > maxKeys {
		for k, old := range l.seen {
			if old.start != start {
				delete(l.seen, k)
			}
		}
	}
	return w.count <= int64(max)
}
