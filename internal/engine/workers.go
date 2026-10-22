package engine

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dyna-agent/internal/goalkeeper"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

// Each signal holds one pending tick. Store scans keep running while a worker
// is busy, and bursts coalesce without losing changes to another store.
type sessionInputs struct {
	// all asks for a pass over every session at startup.
	all                   bool
	targets, rooms, roots map[string]bool
}

type workerSignals struct {
	channels map[string]chan struct{}
	mu       sync.Mutex
	sessions sessionInputs
	// recheckInstructions is set while the worker loop runs.
	recheckInstructions func()
	// sweeps marks the workers whose next pass is a safety sweep. An event
	// for the worker clears its mark, so a pass with an event counts nothing.
	sweeps map[string]bool
	// sweepFindings counts what safety sweeps delivered: session turns,
	// answers and hook events that no event delivered.
	sweepFindings atomic.Int64
}

func newWorkerSignals() *workerSignals {
	return &workerSignals{channels: map[string]chan struct{}{
		"prs": make(chan struct{}, 1), "goals": make(chan struct{}, 1),
		"questions": make(chan struct{}, 1), "hooks": make(chan struct{}, 1),
		"sessions": make(chan struct{}, 1), "merges": make(chan struct{}, 1),
		"sync": make(chan struct{}, 1), "instructions": make(chan struct{}, 1),
		"fetch": make(chan struct{}, 1),
	}, sweeps: map[string]bool{}}
}

func (s *workerSignals) wake(ev roomsvc.Event) {
	if ((ev.Kind == "prs" || ev.Kind == "goals") && ev.WorkerWake) || ev.Kind == "room.resumed" {
		wakeWorker(s.channels["merges"])
	}
	if ev.Kind == "goals" && ev.WorkerWake {
		wakeWorker(s.channels["prs"])
	}
	// A paused room's check-ins wait for its resume.
	if ev.Kind == "room.resumed" {
		wakeWorker(s.channels["goals"])
	}
	s.mu.Lock()
	if s.sessions.targets == nil {
		s.sessions.targets = map[string]bool{}
		s.sessions.rooms = map[string]bool{}
		s.sessions.roots = map[string]bool{}
	}
	switch ev.Kind {
	case "posts":
		add := func(address string, ids map[string]string) {
			if id := ids[address]; id != "" {
				s.sessions.targets[id] = true
			} else if strings.Contains(address, ":") {
				s.sessions.targets[address] = true
			}
		}
		for _, post := range ev.Posts {
			if post.Wake {
				for _, address := range post.To {
					if !strings.Contains(address, ":") && room.WakesRoot(post.Message, address) {
						s.sessions.roots[ev.RoomID] = true
					} else {
						add(address, post.SessionIDs)
					}
				}
			}
			// Bound turn-end senders recover queued input and wrap-up work.
			// Unbound senders use the exact IDs from member changes.
			if post.Turn > 0 && (post.Kind == "completed" || post.Kind == "failed" || post.Kind == "stopped") {
				if id := post.SessionIDs[post.From]; id != "" {
					s.sessions.targets[id] = true
				}
			}
		}
	case "members":
		if ev.Change != nil && ev.Change.Op != "delete" && runstore.IsSessionDirID(ev.Change.ID) {
			s.sessions.targets[ev.Change.ID] = true
		}
	case "room.resumed":
		s.sessions.rooms[ev.RoomID] = true
	}
	pending := len(s.sessions.targets) > 0 || len(s.sessions.rooms) > 0 || len(s.sessions.roots) > 0
	s.mu.Unlock()
	if pending {
		wakeWorker(s.channels["sessions"])
	}
	kind := ev.Kind
	if kind == "changes" {
		kind = "prs"
	}
	if ch := s.channels[kind]; ch != nil && ev.WorkerWake && kind != "sessions" {
		s.mu.Lock()
		delete(s.sweeps, kind)
		s.mu.Unlock()
		wakeWorker(ch)
	}
	// A rule or question change can start an instruction note's quiet
	// period, which the engine may not have written itself.
	// TODO: legacy/shim remove later — rechecks instruction notes after a rule or question event because a CLI writes the quiet period directly; remove when item 2 moves those writes into the engine, which can then arm the exact due time.
	if kind == "goals" || kind == "questions" {
		s.mu.Lock()
		recheck := s.recheckInstructions
		s.mu.Unlock()
		if recheck != nil {
			recheck()
		}
	}
}

// wakeInstructionNotes makes the engine's Manager wake the instruction-note
// worker whenever it stores a quiet period, so an engine-side change (a
// profile edit, say) does not depend on which event, if any, it raises. That
// pass arms the timer at the earliest due note. Call it before the engine
// serves, since the Manager reads the hook without a lock.
func wakeInstructionNotes(m *subagent.Manager, signals *workerSignals) {
	m.InstructionsChanged = func() { wakeWorker(signals.channels["instructions"]) }
}

func (s *workerSignals) setRecheckInstructions(fn func()) {
	s.mu.Lock()
	s.recheckInstructions = fn
	s.mu.Unlock()
}

func (s *workerSignals) takeSessions() sessionInputs {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.sessions
	s.sessions = sessionInputs{}
	return batch
}

// recoverSessions asks for a pass over every session, to catch up on writes
// made while the engine was down.
func (s *workerSignals) recoverSessions() {
	s.mu.Lock()
	s.sessions.all = true
	s.mu.Unlock()
	wakeWorker(s.channels["sessions"])
}

// sweep wakes worker for a safety sweep pass. Events clear the mark, except
// for session delivery: its sweep pass delivers the batch's own targets
// first, so input that an event did report is not counted.
func (s *workerSignals) sweep(worker string) {
	s.mu.Lock()
	s.sweeps[worker] = true
	s.mu.Unlock()
	wakeWorker(s.channels[worker])
}

// takeSweep reports whether this pass of worker is a safety sweep with no
// event since.
func (s *workerSignals) takeSweep(worker string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sweep := s.sweeps[worker]
	delete(s.sweeps, worker)
	return sweep
}

// found counts and logs what a safety sweep pass of worker delivered, by the
// IDs of the sessions or rooms it was for.
func (s *workerSignals) found(worker string, ids []string) {
	if len(ids) == 0 {
		return
	}
	s.sweepFindings.Add(int64(len(ids)))
	log.Printf("safety sweep: %s delivered input that no event delivered, for %s", worker, strings.Join(ids, ", "))
}

// findings is the Health count of safety-sweep findings. A nil s has none.
func (s *workerSignals) findings() int64 {
	if s == nil {
		return 0
	}
	return s.sweepFindings.Load()
}

func wakeWorker(signal chan struct{}) {
	select {
	case signal <- struct{}{}:
	default:
	}
}

type workers struct {
	sweep, sessions func(context.Context) error
	// Timed workers return when they next have work, or zero, and a timer
	// wakes them then: goals runs goal check-ins, instructions sends due
	// instruction notes, fetch fetches local lane remotes, questions
	// retries failed answer deliveries, hooks retries a hook spool drain
	// that could not finish, and prs, merges and sync poll GitHub, merge
	// trials and sync operations while they have work.
	goals, instructions, fetch, questions, hooks, prs, merges, sync func(context.Context) (time.Time, error)
}

func engineWorkers(rooms *roomsvc.Service, signals *workerSignals) workers {
	prs := watch.New()
	prs.Notify = rooms.Notify
	prs.GH.Rooms = rooms.Notify.Rooms
	syncer := lanesync.New()
	spool := hooks.New()
	goals := goalkeeper.New()
	goals.Notify = rooms.Notify
	return workers{
		prs:   prs.Tick,
		goals: goals.Tick,
		questions: func(ctx context.Context) (time.Time, error) {
			sweep := signals.takeSweep("questions")
			var delivered []string
			next, err := rooms.Notify.QuestionStore().Tick(ctx, time.Now(), func(ctx context.Context, room string) error {
				err := rooms.Notify.DeliverAnswers(ctx, room)
				if err == nil {
					delivered = append(delivered, room)
				}
				return err
			})
			if sweep {
				signals.found("question delivery", delivered)
			}
			return next, err
		},
		sweep: rooms.Notify.Sessions.SweepDormantSessions,
		hooks: func(ctx context.Context) (time.Time, error) {
			sweep := signals.takeSweep("hooks")
			ran, next, err := spool.Drain(ctx, time.Now())
			if sweep {
				signals.found("hook spool", ran)
			}
			return next, err
		},
		sessions: func(ctx context.Context) error { return deliverSessions(ctx, rooms.Notify.Sessions, signals) },
		merges:   prs.MergeTick,
		sync:     prs.SyncTick,
		fetch:    func(ctx context.Context) (time.Time, error) { return syncer.Tick(ctx, time.Now()) },
		instructions: func(ctx context.Context) (time.Time, error) {
			return rooms.Notify.Sessions.FlushInstructionNotes(ctx, time.Now())
		},
	}
}

func deliverSessions(ctx context.Context, m *subagent.Manager, signals *workerSignals) error {
	batch := signals.takeSessions()
	sweep := signals.takeSweep("sessions")
	if batch.all && !sweep {
		_, err := m.TickSessions(ctx)
		return err
	}
	var failures []error
	for roomID := range batch.rooms {
		failures = append(failures, m.TickSessionRoom(ctx, roomID))
	}
	targets := make([]string, 0, len(batch.targets)+len(batch.roots))
	for roomID := range batch.roots {
		root, open, err := m.RootSession(roomID)
		if err != nil {
			failures = append(failures, err)
		} else if open {
			targets = append(targets, root.ID)
		}
	}
	for ref := range batch.targets {
		targets = append(targets, ref)
	}
	failures = append(failures, m.TickSessionTargets(ctx, targets))
	// The sweep runs after the batch's own targets, so input that an event
	// did report is not a finding.
	if sweep {
		started, err := m.TickSessions(ctx)
		failures = append(failures, err)
		signals.found("session delivery", started)
	}
	return errors.Join(failures...)
}

// runWorker keeps one worker serial with itself. Other workers never wait for
// this tick, including when a keeper pass waits for its judge.
func runWorker(ctx context.Context, signal <-chan struct{}, tick func(context.Context) error, report func(error)) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-signal:
			if ctx.Err() != nil {
				return
			}
			if err := tick(ctx); err != nil && ctx.Err() == nil {
				report(err)
			}
		}
	}
}

// Periods of the worker loop's timers.
const (
	// safetySweepEvery checks the event-driven workers: with every write in
	// the engine, a sweep should find nothing.
	safetySweepEvery = 10 * time.Minute
	sweepEvery       = 24 * time.Hour
	// instructionRecheck follows a rule or question event: by then a CLI
	// that wrote the change has also written its quiet period.
	// TODO: legacy/shim remove later — only the CLI-written quiet period needs it; remove when item 2 moves those writes into the engine.
	instructionRecheck = 4 * time.Second
)

// run only routes signals and timers. Each worker, including the daily sweep,
// runs in its own goroutine. Timers wake each timed worker at its due time,
// the workers for the 10-minute safety sweep, and the daily sweep; tests fire
// them on demand.
func (w workers) run(ctx context.Context, signals *workerSignals, t *timers, report func(string, error)) {
	var running sync.WaitGroup
	start := func(name string, signal <-chan struct{}, tick func(context.Context) error) {
		running.Add(1)
		go func() {
			defer running.Done()
			runWorker(ctx, signal, tick, func(err error) { report(name, err) })
		}()
	}
	// timed starts a worker that returns its next due time, and wakes it
	// then through key.
	timed := func(name string, signal chan struct{}, key string, tick func(context.Context) (time.Time, error)) {
		start(name, signal, func(ctx context.Context) error {
			next, err := tick(ctx)
			if !next.IsZero() {
				t.at(key, next, func() { wakeWorker(signal) })
			}
			return err
		})
	}
	instructions := signals.channels["instructions"]
	wakeInstructions := func() { wakeWorker(instructions) }
	timed("Instruction notes", instructions, timerInstructions, w.instructions)
	timed("Lane sync", signals.channels["sync"], timerSync, w.sync)
	timed("Lane fetch", signals.channels["fetch"], timerLaneFetch, w.fetch)
	sweep := make(chan struct{}, 1)
	timed("PR watcher", signals.channels["prs"], timerPRs, w.prs)
	timed("Merge service", signals.channels["merges"], timerMerges, w.merges)
	timed("Goal keeper", signals.channels["goals"], timerGoals, w.goals)
	timed("Question delivery", signals.channels["questions"], timerQuestions, w.questions)
	start("Dormant session sweep", sweep, w.sweep)
	timed("Hook spool", signals.channels["hooks"], timerHooks, w.hooks)
	start("Session delivery", signals.channels["sessions"], w.sessions)
	// Its own key, so it never moves a note that is due sooner.
	// TODO: legacy/shim remove later — the recheck for quiet periods that a CLI writes directly; remove with the recheck in workerSignals.wake when item 2 moves those writes into the engine.
	signals.setRecheckInstructions(func() {
		t.at(timerInstructionsRecheck, t.now().Add(instructionRecheck), wakeInstructions)
	})
	defer signals.setRecheckInstructions(nil)
	defer running.Wait()
	wake := func(names ...string) {
		for _, name := range names {
			wakeWorker(signals.channels[name])
		}
	}
	var safety, daily func()
	safety = func() {
		signals.sweep("sessions")
		wake("goals", "instructions", "prs", "merges", "sync")
		signals.sweep("questions")
		signals.sweep("hooks")
		t.at(timerSafetySweep, t.now().Add(safetySweepEvery), safety)
	}
	daily = func() { wakeWorker(sweep); t.at(timerSweep, t.now().Add(sweepEvery), daily) }
	// Catch writes made while the engine was down.
	signals.recoverSessions()
	wake("goals", "instructions", "fetch", "questions", "hooks", "prs", "merges", "sync")
	wakeWorker(sweep)
	t.at(timerSafetySweep, t.now().Add(safetySweepEvery), safety)
	t.at(timerSweep, t.now().Add(sweepEvery), daily)
	<-ctx.Done()
}

// runOwned runs the workers only while this engine owns the store's worker
// lock, the one owner per store that the old daemons' watch locks kept. Two
// engines can share a store when their --addr and --card-addr differ, and only
// one may run the read, poll and apply steps of a tick. A second engine runs no
// workers. Ownership is released only after every worker has stopped.
func runOwned(ctx context.Context, root string, w workers, signals *workerSignals, t *timers, report func(string, error)) (bool, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return false, err
	}
	unlock, owned, err := store.TryLock(filepath.Join(root, "workers.lock"))
	if err != nil || !owned {
		return false, err
	}
	defer unlock()
	w.run(ctx, signals, t, report)
	return true, nil
}

func runWorkers(ctx context.Context, rooms *roomsvc.Service, signals *workerSignals, t *timers) {
	report := func(name string, err error) {
		log.Print(watch.Sanitize(name + ": " + err.Error()))
	}
	owned, err := runOwned(ctx, store.DataDir(), engineWorkers(rooms, signals), signals, t, report)
	if err != nil {
		log.Print(watch.Sanitize("workers: " + err.Error()))
		return
	}
	if !owned {
		log.Print("workers: another engine owns this store, so this engine runs none")
	}
}
