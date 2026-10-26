package engine

import (
	"context"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/database"
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
)

func awaitWorker(t *testing.T, calls <-chan string, names ...string) {
	t.Helper()
	pending := map[string]bool{}
	for _, name := range names {
		pending[name] = true
	}
	for range names {
		select {
		case name := <-calls:
			if !pending[name] {
				t.Fatalf("unexpected %s tick; waiting for %v", name, pending)
			}
			delete(pending, name)
		case <-time.After(2 * time.Second):
			t.Fatalf("missing ticks: %v", pending)
		}
	}
}

// The worker loop has no clock tick: timers wake the startup catch-up, each
// instruction note at its due time, every worker for the 10-minute safety
// sweep, and the daily sweep.
func TestWorkersStartupStoreEventsSafetyAndDailySweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan string, 32)
	fn := func(name string) func(context.Context) error {
		return func(context.Context) error { calls <- name; return nil }
	}
	w := testWorkers(fn)
	clock := newFakeClock()
	// The first pass finds a note due in 3 s; the later passes find none.
	due := clock.Now().Add(3 * time.Second)
	w.instructions = func(context.Context) (time.Time, error) {
		calls <- "instructions"
		next := due
		due = time.Time{}
		return next, nil
	}
	signals := newWorkerSignals(nil)
	set := clock.timers(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx, signals, set, func(string, error) { t.Error("unexpected worker error") })
	}()
	defer func() { cancel(); <-done }()
	awaitWorker(t, calls, "prs", "questions", "sweep", "hooks", "hook-schedule", "sessions", "merges", "instructions", "sync", "fetch", "probe", "caches")
	// The daily sweep's deadline counts from when it is set, so the clock
	// moves only after both sweep timers exist.
	awaitTimer(t, set, timerSafetySweep)
	awaitTimer(t, set, timerSweep)
	awaitTimer(t, set, timerInstructions)
	clock.advance(2 * time.Second)
	expectNoWorker(t, calls)
	clock.advance(time.Second)
	awaitWorker(t, calls, "instructions")
	for _, kind := range []string{"prs", "goals", "questions", "hooks"} {
		signals.wake(roomsvc.Event{Kind: kind, WorkerWake: true})
		if kind == "prs" {
			awaitWorker(t, calls, "prs", "merges")
		} else if kind == "goals" {
			awaitWorker(t, calls, "prs", "merges")
		} else {
			awaitWorker(t, calls, kind)
		}
	}
	// A rule or question change checks instruction notes once, a few seconds
	// later.
	awaitTimer(t, set, timerInstructionsRecheck)
	clock.advance(instructionRecheck)
	awaitWorker(t, calls, "instructions")
	signals.wake(roomsvc.Event{Kind: "prs"})
	signals.wake(roomsvc.Event{Kind: "posts", Posts: []roomsvc.Post{{Message: room.Message{Wake: true, To: []string{"work:target"}}}}})
	awaitWorker(t, calls, "sessions")
	// With no minute wake, the workers wait for the safety sweep, and the
	// day ends with the daily sweep.
	clock.advance(safetySweepEvery - 3*time.Second - instructionRecheck - time.Second)
	expectNoWorker(t, calls)
	clock.advance(time.Second)
	awaitWorker(t, calls, "sessions", "instructions", "questions", "hooks", "prs", "merges", "sync")
	for _, worker := range []string{"sessions", "questions", "hooks"} {
		if !signals.takeSweep(worker) {
			t.Fatalf("the safety sweep did not mark the %s pass", worker)
		}
	}
	set.stop(timerSafetySweep)
	clock.advance(sweepEvery - safetySweepEvery)
	awaitWorker(t, calls, "sweep")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker loop did not stop")
	}
	expectNoWorker(t, calls)
}

// awaitTimer waits until a worker has set key, since workers run in their
// own goroutines.
func awaitTimer(t *testing.T, set *timers, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		set.mu.Lock()
		_, ok := set.m[key]
		set.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timer %s was not set", key)
		}
		runtime.Gosched()
	}
}

func expectNoWorker(t *testing.T, calls <-chan string) {
	t.Helper()
	select {
	case name := <-calls:
		t.Fatalf("extra %s tick", name)
	default:
	}
}

func TestWorkerReportsFailureAndRetriesNextSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan struct{}, 1)
	failure := errors.New("poll failed")
	reports := make(chan error, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWorker(ctx, "prs", signals, func(context.Context) error { return failure }, func(err error) { reports <- err })
	}()
	defer func() { cancel(); <-done }()
	for i := 0; i < 2; i++ {
		signals <- struct{}{}
		select {
		case err := <-reports:
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("missing error report")
		}
	}
}

func TestWorkersCancelledBeforeStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fail := func(context.Context) error { t.Error("cancelled loop ran worker"); return nil }
	testWorkers(func(string) func(context.Context) error { return fail }).run(ctx, newWorkerSignals(nil), newFakeClock().timers(ctx), func(string, error) {})
}

func TestOneWorkerOwnerPerStore(t *testing.T) {
	root := t.TempDir()
	calls := make(chan string, 32)
	w := func(owner string) workers {
		tick := func(name string) func(context.Context) error {
			return func(context.Context) error { calls <- owner + "-" + name; return nil }
		}
		w := testWorkers(tick)
		// Only the engine that owns the store settles the previous engine's runs.
		w.settle = func(context.Context) error { calls <- owner + "-settle"; return nil }
		return w
	}
	report := func(string, error) { t.Error("unexpected worker error") }
	run := func(ctx context.Context, owner string) <-chan bool {
		done := make(chan bool, 1)
		go func() {
			owned, err := runOwned(ctx, root, w(owner), newWorkerSignals(nil), newFakeClock().timers(ctx), report)
			if err != nil {
				t.Error(err)
			}
			done <- owned
		}()
		return done
	}
	stop := func(done <-chan bool, owner string) {
		t.Helper()
		select {
		case owned := <-done:
			if !owned {
				t.Fatalf("%s engine does not own the store workers", owner)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s owner did not stop", owner)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := run(ctx, "first")
	awaitWorker(t, calls, "first-settle", "first-hook-schedule", "first-prs", "first-questions", "first-sweep", "first-hooks", "first-sessions", "first-merges", "first-instructions", "first-sync", "first-fetch", "first-probe", "first-caches")
	// A second engine on the same store runs no workers at all.
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan bool, 1)
	go func() {
		owned, err := runOwned(secondCtx, root, w("second"), newWorkerSignals(nil), newFakeClock().timers(secondCtx), report)
		if err != nil {
			t.Error(err)
		}
		secondDone <- owned
	}()
	select {
	case owned := <-secondDone:
		if owned {
			t.Fatal("second engine owns the store workers")
		}
	case <-time.After(2 * time.Second):
		cancelSecond()
		<-secondDone
		t.Fatal("second engine ran workers for this store")
	}
	cancelSecond()
	select {
	case name := <-calls:
		t.Fatalf("second owner ran %s", name)
	default:
	}
	cancel()
	stop(first, "first")
	// Ownership is released only after every worker stopped, so a new engine
	// takes over the store workers.
	ctx, cancel = context.WithCancel(context.Background())
	third := run(ctx, "third")
	awaitWorker(t, calls, "third-settle", "third-hook-schedule", "third-prs", "third-questions", "third-sweep", "third-hooks", "third-sessions", "third-merges", "third-instructions", "third-sync", "third-fetch", "third-probe", "third-caches")
	cancel()
	stop(third, "third")
	select {
	case name := <-calls:
		t.Fatalf("extra %s tick", name)
	default:
	}
}

func TestEngineHookWorkerDrainsStartupSpoolOnce(t *testing.T) {
	// Native guard subprocesses need race checks, but no exit grace period.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	services := testServices(t, false)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "initial"},
	} {
		if _, err := repoid.Git(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	h := hooks.Hook{Name: "queued", Event: hooks.AfterLand, Command: "true", Cwd: "worktree", Timeout: "30s", OnFail: "alert"}
	if err := services.hooks.Add(ctx, repo, h); err != nil {
		t.Fatal(err)
	}
	if err := services.hooks.Emit(ctx, hooks.Event{Repo: repo, Event: hooks.AfterLand}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.InEngine())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := engineWorkers(services.rooms, newWorkerSignals(nil), nil, nil, services.hooks, time.Now())
	drain := w.hooks
	ticks := make(chan struct{}, 4)
	w.hooks = func(ctx context.Context) (time.Time, error) {
		next, err := drain(ctx)
		ticks <- struct{}{}
		return next, err
	}
	idle := func(context.Context) error { return nil }
	w.sweep, w.sessions = idle, idle
	idleTimed := func(context.Context) (time.Time, error) { return time.Time{}, nil }
	w.questions, w.prs = idleTimed, idleTimed
	// A machine probe runs a shell script that samples CPU for a second, and the
	// worker wait for it on shutdown. Probing a machine is not what this test
	// checks, so it stays idle.
	w.probe = idleTimed
	signals := newWorkerSignals(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx, signals, newFakeClock().timers(ctx), func(name string, err error) { t.Errorf("%s: %v", name, err) })
	}()
	defer func() {
		cancel()
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			t.Error("hook worker did not stop")
		}
	}()
	await := func(phase string) {
		t.Helper()
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-ticks:
		case <-done:
			t.Fatalf("hook worker stopped before %s", phase)
		case <-timer.C:
			t.Fatalf("hook worker did not finish %s", phase)
		}
	}
	await("startup drain") // Drains events left while the engine was down.
	signals.wake(roomsvc.Event{Kind: "hooks", WorkerWake: true})
	await("explicit wake")
	runs, err := services.hooks.Runs(ctx, repo, h.Name)
	if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
		t.Fatalf("dispatches: %#v; %v", runs, err)
	}
}

func TestSessionSignalsRetainTargetsAcrossPasses(t *testing.T) {
	signals := newWorkerSignals(nil)
	post := func(address string) {
		signals.wake(roomsvc.Event{Kind: "posts", Posts: []roomsvc.Post{{Message: room.Message{Wake: true, To: []string{address}}}}})
	}
	post("work:a")
	post("work:a")
	post("work:b")
	first := signals.takeSessions()
	if first.all || len(first.targets) != 2 || !first.targets["work:a"] || !first.targets["work:b"] {
		t.Fatalf("first batch: %+v", first)
	}
	post("work:c") // Arrives while the previous batch is running.
	second := signals.takeSessions()
	if len(second.targets) != 1 || !second.targets["work:c"] {
		t.Fatalf("next batch lost its target: %+v", second)
	}
	signals.wake(roomsvc.Event{Kind: "room.resumed", RoomID: "room-one"})
	resume := signals.takeSessions()
	if len(resume.rooms) != 1 || !resume.rooms["room-one"] || resume.all {
		t.Fatalf("resume must select only its room: %+v", resume)
	}
	signals.wake(roomsvc.Event{Kind: "members", RoomID: "room-two"})
	if batch := signals.takeSessions(); batch.all || len(batch.rooms) != 0 || len(batch.targets) != 0 {
		t.Fatalf("member progress selected sessions: %+v", batch)
	}
	signals.recoverSessions()
	if batch := signals.takeSessions(); !batch.all || signals.takeSweep("sessions") {
		t.Fatalf("startup recovery: %+v", batch)
	}
}

func TestSessionSignalsSelectBoundIDsRootAndChangedMember(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rooms := room.DefaultStore()
	t.Cleanup(func() {
		if err := database.Close(filepath.Dir(rooms.Root)); err != nil {
			t.Error(err)
		}
	})
	rec, err := rooms.Create(room.CreateInput{Name: "work", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var posts []roomsvc.Post
	for _, in := range []room.PostInput{
		{Wake: true, From: "work:lead", To: []string{"work:alias"}, SessionIDs: map[string]string{"work:alias": "exact-session"}, Text: "for the alias"},
		{Wake: true, Human: true, From: "work", To: []string{"work"}, Text: "owner request"},
		{Wake: false, From: "work:lead", To: []string{"work:quiet"}, Text: "quiet"},
	} {
		msg, err := rooms.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, roomsvc.Post{Message: msg})
	}
	signals := newWorkerSignals(rooms)
	signals.wake(roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: posts})
	signals.wake(roomsvc.Event{Kind: "members", Change: &listchange.Change{Op: "upsert", ID: "ses_20261018-120000-abcdef12"}})
	signals.wake(roomsvc.Event{Kind: "members", Change: &listchange.Change{Op: "delete", ID: "removed-session"}})
	batch := signals.takeSessions()
	if len(batch.targets) != 2 || len(batch.roots) != 1 || !batch.roots[rec.ID] || !batch.targets["exact-session"] || !batch.targets["ses_20261018-120000-abcdef12"] {
		t.Fatalf("wrong event recipients: %+v", batch)
	}
}

func TestSessionSignalsIgnoreCanonicalOwnerRow(t *testing.T) {
	signals := newWorkerSignals(nil)
	signals.wake(roomsvc.Event{Kind: "members", Change: &listchange.Change{Op: "upsert", ID: "work"}})
	select {
	case <-signals.channels["sessions"]:
		t.Fatal("owner row signalled session delivery")
	default:
	}
	batch := signals.takeSessions()
	if len(batch.targets) != 0 {
		t.Fatalf("owner row became a session target: %+v", batch)
	}
}

func TestChangesItemEventsUseTheExistingPRWorkerSignal(t *testing.T) {
	signals := newWorkerSignals(nil)
	signals.wake(roomsvc.Event{Kind: "changes"})
	select {
	case <-signals.channels["prs"]:
		t.Fatal("display-only event woke the PR worker")
	default:
	}
	signals.wake(roomsvc.Event{Kind: "changes", WorkerWake: true})
	select {
	case <-signals.channels["prs"]:
	default:
		t.Fatal("changes event did not wake the existing PR worker")
	}
	if signals.channels["changes"] != nil {
		t.Fatal("changes added another worker channel")
	}
}

// One timer holds the earliest due instruction note. A new or changed note,
// seen on the pass that a rule event triggers, re-arms it to the new due time,
// and the old deadline does not fire.
func TestNewInstructionNoteReArmsTheTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan string, 32)
	w := testWorkers(nil)
	clock := newFakeClock()
	start := clock.Now()
	// Each pass reports the earliest note still waiting: 10 s at first, then
	// a newer note due at 6 s, then none.
	dues := []time.Time{start.Add(10 * time.Second), start.Add(6 * time.Second)}
	w.instructions = func(context.Context) (time.Time, error) {
		calls <- "instructions"
		if len(dues) == 0 {
			return time.Time{}, nil
		}
		next := dues[0]
		dues = dues[1:]
		return next, nil
	}
	signals := newWorkerSignals(nil)
	set := clock.timers(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx, signals, set, func(string, error) { t.Error("unexpected worker error") })
	}()
	defer func() { cancel(); <-done }()
	awaitWorker(t, calls, "instructions")
	awaitTimer(t, set, timerInstructions)
	awaitTimer(t, set, timerSafetySweep)
	signals.wake(roomsvc.Event{Kind: "goals"})
	clock.advance(instructionRecheck)
	awaitWorker(t, calls, "instructions")
	awaitTimerAt(t, set, clock, timerInstructions, start.Add(6*time.Second))
	clock.advance(time.Second)
	expectNoWorker(t, calls)
	clock.advance(time.Second)
	awaitWorker(t, calls, "instructions")
	clock.advance(5 * time.Second)
	expectNoWorker(t, calls)
}

// awaitTimerAt waits until key is set to fire at when.
func awaitTimerAt(t *testing.T, set *timers, clock *fakeClock, key string, when time.Time) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if clock.pendingAt(when) {
			set.mu.Lock()
			_, ok := set.m[key]
			set.mu.Unlock()
			if ok {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timer %s was not set for %v", key, when)
		}
		runtime.Gosched()
	}
}

// A profile change through the engine's Manager wakes the instruction-note
// worker with no rule or question event, so its note is armed at its due time
// instead of waiting for the safety sweep.
func TestEngineProfileChangeArmsItsNote(t *testing.T) {
	storetest.DataDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{
		{Name: "mock", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5},
		{Name: "writer", Harness: profile.HarnessMock, Model: "model-2", Taste: 7, Intelligence: 8, Cost: 6},
	}}
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan string, 32)
	w := testWorkers(nil)
	clock := newFakeClock()
	var mu sync.Mutex
	due := time.Time{}
	w.instructions = func(context.Context) (time.Time, error) {
		calls <- "instructions"
		mu.Lock()
		defer mu.Unlock()
		next := due
		due = time.Time{}
		return next, nil
	}
	signals := newWorkerSignals(nil)
	sessions := subagent.NewManager()
	wakeInstructionNotes(sessions, signals)
	config := &configsvc.Service{Sessions: sessions}
	set := clock.timers(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx, signals, set, func(string, error) { t.Error("unexpected worker error") })
	}()
	defer func() { cancel(); <-done }()
	awaitWorker(t, calls, "instructions")
	awaitTimer(t, set, timerSafetySweep)
	mu.Lock()
	due = clock.Now().Add(3 * time.Second)
	mu.Unlock()
	if err := config.SetDefaultProfile(ctx, "writer"); err != nil {
		t.Fatal(err)
	}
	awaitWorker(t, calls, "instructions")
	awaitTimerAt(t, set, clock, timerInstructions, clock.Now().Add(3*time.Second))
	clock.advance(3 * time.Second)
	awaitWorker(t, calls, "instructions")
	expectNoWorker(t, calls)
}

// The lane fetch runs at its own due time, not on the minute wake.
func TestLaneFetchWakesAtItsDueTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan string, 32)
	w := testWorkers(nil)
	clock := newFakeClock()
	w.fetch = func(context.Context) (time.Time, error) {
		calls <- "fetch"
		return clock.Now().Add(5 * time.Minute), nil
	}
	set := clock.timers(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx, newWorkerSignals(nil), set, func(string, error) { t.Error("unexpected worker error") })
	}()
	defer func() { cancel(); <-done }()
	awaitWorker(t, calls, "fetch")
	awaitTimerAt(t, set, clock, timerLaneFetch, clock.Now().Add(5*time.Minute))
	clock.advance(5*time.Minute - time.Second)
	expectNoWorker(t, calls)
	clock.advance(time.Second)
	awaitWorker(t, calls, "fetch")
}

// A failed answer delivery is retried at the time the question worker asks
// for, not on the minute wake.
func TestQuestionDeliveryRetriesAtItsDueTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan string, 32)
	w := testWorkers(nil)
	clock := newFakeClock()
	retry := clock.Now().Add(time.Minute)
	w.questions = func(context.Context) (time.Time, error) {
		calls <- "questions"
		next := retry
		retry = time.Time{}
		return next, nil
	}
	set := clock.timers(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx, newWorkerSignals(nil), set, func(string, error) { t.Error("unexpected worker error") })
	}()
	defer func() { cancel(); <-done }()
	awaitWorker(t, calls, "questions")
	awaitTimerAt(t, set, clock, timerQuestions, clock.Now().Add(time.Minute))
	clock.advance(time.Minute - time.Second)
	expectNoWorker(t, calls)
	clock.advance(time.Second)
	awaitWorker(t, calls, "questions")
	// Delivered: no retry, and the minute wake no longer runs it.
	clock.advance(2 * time.Minute)
	expectNoWorker(t, calls)
}

// A safety sweep pass counts what it delivers; an event for the worker
// before the pass clears the mark, so that pass counts nothing.
func TestSweepMarkCountsOnlyAPassWithNoEvent(t *testing.T) {
	signals := newWorkerSignals(nil)
	signals.sweep("questions")
	if !signals.takeSweep("questions") || signals.takeSweep("questions") {
		t.Fatal("a sweep mark must last for one pass")
	}
	signals.found("question delivery", []string{"room-a"})
	signals.sweep("questions")
	signals.wake(roomsvc.Event{Kind: "questions", WorkerWake: true})
	if signals.takeSweep("questions") {
		t.Fatal("a pass with an event was counted as a sweep")
	}
	signals.found("question delivery", nil)
	if got := signals.findings(); got != 1 {
		t.Fatalf("findings = %d, want 1", got)
	}
}

// Settlement measures a run against the engine's own start, not the moment the
// workers reach it. Startup recovery can take time, and a hook run that the
// owner starts in between must stay live.
func TestWorkersSettleFromTheEngineStart(t *testing.T) {
	services := testServices(t, false)
	started := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	var cutoff time.Time
	settleHookRuns = func(_ context.Context, at time.Time) error {
		cutoff = at
		return nil
	}
	t.Cleanup(func() { settleHookRuns = settleFromStart })
	w := engineWorkers(services.rooms, newWorkerSignals(nil), nil, nil, services.hooks, started)
	if err := w.settle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !cutoff.Equal(started) {
		t.Fatalf("settle cutoff = %s, want the engine start %s", cutoff, started)
	}
}

// Resuming a paused room makes its check-ins due again, as a turn end does: the
// hooks worker runs one pass through the hook's own switch.
func TestResumedRoomRunsACheckInPass(t *testing.T) {
	signals := newWorkerSignals(nil)
	signals.wake(roomsvc.Event{Kind: "room.resumed", RoomID: "room_1"})
	if !signals.takeRoomResumed() {
		t.Fatal("a resumed room did not mark a check-in pass")
	}
	if signals.takeRoomResumed() {
		t.Fatal("the resume mark survived one pass")
	}
}

// A pass that blocks ends at its deadline and the worker takes the next signal,
// and a pass that runs past the warning threshold is reported once.
func TestWorkerPassIsBoundedAndReported(t *testing.T) {
	previousTimeout, previousWarn := workerPassTimeout, workerPassWarn
	workerPassTimeout, workerPassWarn = 80*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { workerPassTimeout, workerPassWarn = previousTimeout, previousWarn })

	signal := make(chan struct{}, 1)
	blocked := make(chan struct{}, 1)
	reports := make(chan error, 4)
	var passes atomic.Int32
	tick := func(ctx context.Context) error {
		if passes.Add(1) == 1 {
			blocked <- struct{}{}
			<-ctx.Done() // the pass hangs until its deadline ends it
			return ctx.Err()
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runWorker(ctx, "sessions", signal, tick, func(err error) { reports <- err })
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })

	signal <- struct{}{}
	<-blocked
	select {
	case err := <-reports:
		if !strings.Contains(err.Error(), "sessions pass ended at its") {
			t.Fatalf("report = %v, want the bounded pass", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a blocked pass was not bounded")
	}
	// Delivery goes on: the next signal runs another pass.
	signal <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for passes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if passes.Load() < 2 {
		t.Fatalf("passes = %d, want a second pass after the bounded one", passes.Load())
	}
}
