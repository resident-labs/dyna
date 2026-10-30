package engine

import (
	"context"
	"sync"
	"time"
)

// The keys of the engine's timer set, in one block, so that two owners never
// share a key, and so move each other's deadline, without it showing here.
const (
	timerSafetySweep         = "safety-sweep"
	timerSweep               = "sweep"
	timerInstructions        = "instructions"
	timerInstructionsRecheck = "instructions-recheck"
	timerCallRing            = "call-ring"
	timerCallRetry           = "call-retry"
	timerAccounts            = "accounts"
	timerLaneFetch           = "lane-fetch"
	timerQuestions           = "questions"
	timerHooks               = "hooks"
	timerHookSchedule        = "hook-schedule"
	timerLibrarianWake       = "librarian-wake"
	timerRootFlush           = "root-flush"
	timerPRs                 = "prs"
	timerMerges              = "merges"
	timerCaches              = "ci-caches"
	timerSync                = "sync"
	timerProbe               = "machine-probe"
	timerRequests            = "requests"
	timerApprovals           = "approvals"
	timerMigrationRetry      = "migration-retry"
)

// timers holds the engine's one-shot deadlines by key. Each fires at its real
// due time; setting a key again moves its deadline. The set closes with the
// engine context, and a closed set fires nothing.
type timers struct {
	now   func() time.Time
	after func(time.Duration, func()) (stop func() bool)

	mu     sync.Mutex
	m      map[string]timerEntry
	gen    uint64
	closed bool
	// done closes with the set.
	done chan struct{}
}

type timerEntry struct {
	gen  uint64
	when time.Time
	stop func() bool
}

// newTimers closes the set when ctx ends. Tests pass their own now and after,
// so a fake clock fires timers on demand.
func newTimers(ctx context.Context, now func() time.Time, after func(time.Duration, func()) func() bool) *timers {
	t := &timers{now: now, after: after, m: map[string]timerEntry{}, done: make(chan struct{})}
	context.AfterFunc(ctx, t.close)
	return t
}

// afterFunc is the real clock for newTimers.
func afterFunc(d time.Duration, fn func()) func() bool { return time.AfterFunc(d, fn).Stop }

// at runs fn at when, in place of any earlier deadline for key.
func (t *timers) at(key string, when time.Time, fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if e, ok := t.m[key]; ok {
		e.stop()
	}
	t.gen++
	gen := t.gen
	stop := t.after(when.Sub(t.now()), func() {
		t.mu.Lock()
		// A deadline that was moved, stopped or closed after it started to
		// fire does nothing.
		live := !t.closed && t.m[key].gen == gen
		if live {
			delete(t.m, key)
		}
		t.mu.Unlock()
		if live {
			fn()
		}
	})
	t.m[key] = timerEntry{gen: gen, when: when, stop: stop}
}

// due reports when key's deadline fires, and false when none is set.
func (t *timers) due(key string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[key]
	return e.when, ok
}

// stop drops key's deadline.
func (t *timers) stop(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.m[key]; ok {
		e.stop()
		delete(t.m, key)
	}
}

// close stops every deadline; later calls to at do nothing.
func (t *timers) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	close(t.done)
	for key, e := range t.m {
		e.stop()
		delete(t.m, key)
	}
}
