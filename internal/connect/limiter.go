package connect

import (
	"sync"
	"time"
)

// --- limiter ---

type limiter struct {
	mu  sync.Mutex
	now func() time.Time
	w   map[string][2]int64 // window start (unix s), count
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, w: map[string][2]int64{}}
}

func (l *limiter) allow(key string, max int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := l.now().Truncate(per).Unix()
	v := l.w[key]
	if v[0] != start {
		v = [2]int64{start, 0}
	}
	v[1]++
	l.w[key] = v
	if len(l.w) > 4096 {
		for k, x := range l.w {
			if x[0] != start {
				delete(l.w, k)
			}
		}
	}
	return v[1] <= int64(max)
}
