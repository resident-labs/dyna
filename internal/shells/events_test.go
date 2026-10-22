package shells

import (
	"maps"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Every write to a job row sends one event that names the job, and a read
// with no write returns none.
func TestJobWritesSendEvents(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	events, err := s.WatchEvents()
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	read := func() []string {
		t.Helper()
		ids, err := events.Read()
		if err != nil {
			t.Fatal(err)
		}
		return slices.Sorted(maps.Keys(ids))
	}
	m := Meta{ID: "sh_20261020-103000-aaaaaaaa", Cmd: []string{"true"}, Cwd: "/", Started: time.Now()}
	if err := s.Save(m, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); !slices.Equal(got, []string{m.ID}) {
		t.Fatalf("after a save = %v, want %s", got, m.ID)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("with no write = %v, want none", got)
	}
	if err := s.SetReadOffset(m.ID, 4); err != nil {
		t.Fatal(err)
	}
	if got := read(); !slices.Equal(got, []string{m.ID}) {
		t.Fatalf("after a setter = %v, want %s", got, m.ID)
	}
	// No event file is left behind.
	if left, _ := filepath.Glob(filepath.Join(s.Root, eventsDir, "*")); len(left) != 0 {
		t.Fatalf("event files left: %v", left)
	}
}

// Output sends one event at once, then at most one per interval; the next
// one comes after the last output, even when no more output follows.
func TestOutputEventsAreSpacedAndTrailing(t *testing.T) {
	old := outputEventEvery
	outputEventEvery = 50 * time.Millisecond
	t.Cleanup(func() { outputEventEvery = old })
	sent := make(chan time.Time, 4)
	o := &outputEvents{send: func() { sent <- time.Now() }}
	defer o.stop()
	o.grew()
	first := <-sent
	o.grew()
	o.grew()
	second := <-sent
	if gap := second.Sub(first); gap < outputEventEvery {
		t.Fatalf("second event %v after the first, want at least %v", gap, outputEventEvery)
	}
	if len(sent) != 0 {
		t.Fatalf("%d extra events for output within one interval", len(sent))
	}
}

// A stop waits for an event that already began. Without that join the event
// reaches the store after the supervisor returned, which can create the event
// directory while a caller removes its temporary store, so its cleanup fails
// with "directory not empty" (TestSuperviseMasksMultilineEnvironmentKey).
func TestOutputEventsStopWaitsForItsEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var finished atomic.Bool
		o := &outputEvents{send: func() {
			close(entered)
			<-release
			finished.Store(true)
		}}
		o.grew()
		<-entered // the output callback is running send and blocks on release
		// Unbuffered, so a stop that returns early then blocks handing back its
		// answer instead of racing the released sender.
		stopped := make(chan bool)
		go func() {
			o.stop()
			stopped <- finished.Load()
		}()
		// Every goroutine is now durably blocked: send on release, and stop
		// either in its own wait for that callback or, without the join, on the
		// send above. So this says which one happened, with no scheduler guess.
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("stop returned while its event was still being sent")
		default:
		}
		close(release)
		if got := <-stopped; !got {
			t.Fatal("stop returned before its event finished")
		}
	})
}
