package engine

import (
	"context"
	"testing"
	"time"
)

// testWorkers fills omitted callbacks with no-ops. Keep the full literal
// positional so a new worker field requires updating these defaults here.
func testWorkers(tick func(string) func(context.Context) error) workers {
	noop := func(context.Context) error { return nil }
	callback := func(name string) func(context.Context) error {
		if tick != nil {
			if run := tick(name); run != nil {
				return run
			}
		}
		return noop
	}
	instructions := callback("instructions")
	fetch := callback("fetch")
	questions := callback("questions")
	hooks := callback("hooks")
	timed := func(name string) func(context.Context) (time.Time, error) {
		run := callback(name)
		return func(ctx context.Context) (time.Time, error) { return time.Time{}, run(ctx) }
	}
	return workers{callback("sweep"), callback("sessions"),
		func(ctx context.Context) (time.Time, error) { return time.Time{}, instructions(ctx) },
		func(ctx context.Context) (time.Time, error) { return time.Time{}, fetch(ctx) },
		func(ctx context.Context) (time.Time, error) { return time.Time{}, questions(ctx) },
		func(ctx context.Context) (time.Time, error) { return time.Time{}, hooks(ctx) },
		timed("prs"), timed("merges"), timed("sync"), timed("probe"), timed("caches"), nil}
}

func TestWorkerFixtureDefaultsForMissingCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	// The fixture only needs a PR callback; other workers should be harmless.
	w := testWorkers(func(name string) func(context.Context) error {
		if name != "prs" {
			return nil
		}
		return func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return nil
		}
	})
	if w.sessions == nil {
		t.Fatal("fixture has no default for the omitted session callback")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx, newWorkerSignals(nil), newFakeClock().timers(ctx), func(_ string, err error) { t.Error(err) })
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fixture worker did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fixture workers did not stop")
	}
}
