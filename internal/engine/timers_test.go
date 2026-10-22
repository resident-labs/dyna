package engine

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// fakeClock fires timers when a test advances it, in deadline order, so
// timer tests never sleep.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*fakeTimer
}

type fakeTimer struct {
	at time.Time
	fn func()
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) after(d time.Duration, fn func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTimer{at: c.now.Add(d), fn: fn}
	c.pending = append(c.pending, ft)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		i := slices.Index(c.pending, ft)
		if i < 0 {
			return false
		}
		c.pending = slices.Delete(c.pending, i, i+1)
		return true
	}
}

// advance moves the clock by d and fires every timer due by then.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.pending, func(i, j int) bool { return c.pending[i].at.Before(c.pending[j].at) })
		if len(c.pending) == 0 || c.pending[0].at.After(target) {
			c.now = target
			c.mu.Unlock()
			return
		}
		next := c.pending[0]
		c.pending = c.pending[1:]
		c.now = next.at
		c.mu.Unlock()
		next.fn()
	}
}

func (c *fakeClock) timers(ctx context.Context) *timers { return newTimers(ctx, c.Now, c.after) }

// pendingAt reports whether a timer is set to fire at when.
func (c *fakeClock) pendingAt(when time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.pending, func(ft *fakeTimer) bool { return ft.at.Equal(when) })
}

// Each key fires once at its due time; setting it again moves the deadline,
// stop drops it, and a closed set fires nothing.
func TestTimersFireAtTheirDueTimeByKey(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	clock := newFakeClock()
	set := clock.timers(ctx)
	var fired []string
	start := clock.Now()
	set.at("a", start.Add(3*time.Second), func() { fired = append(fired, "a") })
	set.at("b", start.Add(time.Second), func() { fired = append(fired, "b") })
	set.at("c", start.Add(2*time.Second), func() { fired = append(fired, "c") })
	set.at("a", start.Add(5*time.Second), func() { fired = append(fired, "a moved") })
	set.stop("c")
	clock.advance(4 * time.Second)
	if !slices.Equal(fired, []string{"b"}) {
		t.Fatalf("by 4 s fired %v, want [b]", fired)
	}
	clock.advance(time.Second)
	if !slices.Equal(fired, []string{"b", "a moved"}) {
		t.Fatalf("by 5 s fired %v, want [b a moved]", fired)
	}
	set.at("d", clock.Now().Add(time.Second), func() { fired = append(fired, "d") })
	cancel()
	<-set.done
	clock.advance(time.Minute)
	set.at("e", clock.Now(), func() { fired = append(fired, "e") })
	clock.advance(time.Minute)
	if len(fired) != 2 {
		t.Fatalf("a closed set fired %v", fired[2:])
	}
}

// Credential refresh has no minute tick: each pass says when the next login
// is due, and the next pass runs exactly then.
func TestCredentialRefreshRunsWhenTheNextLoginIsDue(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	clock := newFakeClock()
	set := clock.timers(ctx)
	start := clock.Now()
	passes := make(chan time.Time, 4)
	// The first pass finds a login due in 20 minutes, the second one in 3.
	next := []time.Duration{20 * time.Minute, 23 * time.Minute, time.Hour}
	pass := func(_ context.Context, now time.Time) time.Time {
		passes <- now
		at := start.Add(next[0])
		next = next[1:]
		return at
	}
	done := make(chan struct{})
	go func() { defer close(done); refreshAccounts(ctx, pass, set, hub.New(0)) }()
	defer func() { cancel(); <-done }()
	if at := <-passes; !at.Equal(start) {
		t.Fatalf("first pass at %v, want at start", at)
	}
	awaitTimerAt(t, set, clock, timerAccounts, start.Add(20*time.Minute))
	clock.advance(20*time.Minute - time.Second)
	select {
	case at := <-passes:
		t.Fatalf("a pass ran at %v, before the login was due", at)
	default:
	}
	clock.advance(time.Second)
	if at := <-passes; !at.Equal(start.Add(20 * time.Minute)) {
		t.Fatalf("second pass at %v, want at 20 min", at)
	}
	awaitTimerAt(t, set, clock, timerAccounts, start.Add(23*time.Minute))
	clock.advance(3 * time.Minute)
	if at := <-passes; !at.Equal(start.Add(23 * time.Minute)) {
		t.Fatalf("third pass at %v, want at 23 min", at)
	}
}

// A committed change to the account registry runs a pass at once, without the
// minute-long recheck. An empty save and other topics wake nothing, and the
// pass still sets the deadline it computes.
func TestAccountChangeWakesRefreshPass(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	clock := newFakeClock()
	set := clock.timers(ctx)
	start := clock.Now()
	root := t.TempDir()
	t.Cleanup(func() { database.Close(root) })
	accounts := &accountstore.Store{Root: filepath.Join(root, "accounts")}
	events := hub.New(0)
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	statewrite.For(db).SetPublish(events.Publish)
	passes := make(chan time.Time, 4)
	pass := func(_ context.Context, now time.Time) time.Time {
		passes <- now
		return start.Add(time.Hour)
	}
	done := make(chan struct{})
	go func() { defer close(done); refreshAccounts(ctx, pass, set, events) }()
	defer func() { cancel(); <-done }()
	<-passes
	awaitTimerAt(t, set, clock, timerAccounts, start.Add(time.Hour))
	settled := func() {
		t.Helper()
		for events.Stats().MaxLag != 0 {
			runtime.Gosched()
		}
	}
	update := func(fn func(*accountstore.State) error) {
		t.Helper()
		if err := accounts.Update(fn); err != nil {
			t.Fatal(err)
		}
	}
	update(func(*accountstore.State) error { return nil })
	events.Publish(hub.Event{Topic: "posts"})
	settled()
	if len(passes) != 0 {
		t.Fatal("an empty save or another topic woke a pass")
	}
	update(func(st *accountstore.State) error {
		_, err := st.Save("fixture", "", "account", "Account", accountstore.Credential{Schema: "fixture-v1", Payload: []byte(`{}`)})
		return err
	})
	if at := <-passes; !at.Equal(start) {
		t.Fatalf("pass at %v, want the changed account to run one at once", at)
	}
	awaitTimerAt(t, set, clock, timerAccounts, start.Add(time.Hour))
}
