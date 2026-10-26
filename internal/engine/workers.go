package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dyna-agent/internal/hooks"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/sysload"
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
	// rooms reads the stored delivery rows that decide which session a post
	// reaches. It is set once, when the signals are created, before any worker
	// or watch reads a post event; a nil store reads no rows, so no post then
	// wakes a room's root seat.
	rooms    *room.Store
	mu       sync.Mutex
	sessions sessionInputs
	// turnEnds holds the bound sessions whose turn ended since the hooks
	// worker last fired their session-turn-ended events. One event per session
	// per pass, however many turns it ran in between.
	turnEnds map[string]bool
	// roomResumed marks that a paused room resumed since the hooks worker last
	// ran, so its check-ins are due again.
	roomResumed bool
	// recheckInstructions is set while the worker loop runs.
	recheckInstructions func()
	// sweeps marks the workers whose next pass is a safety sweep. An event
	// for the worker clears its mark, so a pass with an event counts nothing.
	sweeps map[string]bool
	// sweepFindings counts what safety sweeps delivered: session turns,
	// answers and hook events that no event delivered.
	sweepFindings atomic.Int64
}

func newWorkerSignals(rooms *room.Store) *workerSignals {
	return &workerSignals{rooms: rooms, channels: map[string]chan struct{}{
		"prs":       make(chan struct{}, 1),
		"questions": make(chan struct{}, 1), "hooks": make(chan struct{}, 1),
		"sessions": make(chan struct{}, 1), "merges": make(chan struct{}, 1),
		"sync": make(chan struct{}, 1), "instructions": make(chan struct{}, 1),
		"fetch": make(chan struct{}, 1), "probe": make(chan struct{}, 1),
		"caches": make(chan struct{}, 1), "hook-schedule": make(chan struct{}, 1),
	}, sweeps: map[string]bool{}}
}

// seatDeliveries reads the root seat's delivery rows of the posts an event
// reports, so a wake is decided from the stored row and not from the address
// strings. It reads the database outside the signals lock.
func (s *workerSignals) seatDeliveries(ev roomsvc.Event) map[string]room.Delivery {
	if s.rooms == nil || ev.Kind != "posts" || ev.RoomID == "" || len(ev.Posts) == 0 {
		return nil
	}
	ids := make([]string, len(ev.Posts))
	for i, post := range ev.Posts {
		ids[i] = post.Message.ID
	}
	seats, err := s.rooms.SeatDeliveries(ev.RoomID, ids)
	if err != nil {
		log.Printf("sessions: read the root seat's deliveries of room %s: %v", ev.RoomID, err)
		return nil
	}
	return seats
}

func (s *workerSignals) seatAddress(ev roomsvc.Event) string {
	if s.rooms == nil || ev.Kind != "posts" || ev.RoomID == "" {
		return ""
	}
	rec, err := s.rooms.Resolve(ev.RoomID)
	if err != nil {
		log.Printf("sessions: resolve room %s for its seat address: %v", ev.RoomID, err)
		return ""
	}
	return rec.Name
}

func (s *workerSignals) wake(ev roomsvc.Event) {
	if ((ev.Kind == "prs" || ev.Kind == "goals") && ev.WorkerWake) || ev.Kind == "room.resumed" {
		wakeWorker(s.channels["merges"])
	}
	if ev.Kind == "goals" && ev.WorkerWake {
		wakeWorker(s.channels["prs"])
	}
	// The room's root seat takes a post when its stored delivery row wakes it,
	// whatever address named it; every other recipient is a session, taken by its
	// stored row or its address. The room's own address names the seat, so a
	// recipient that carries it is no session target.
	seats := s.seatDeliveries(ev)
	seatAddr := s.seatAddress(ev)
	s.mu.Lock()
	if s.sessions.targets == nil {
		s.sessions.targets = map[string]bool{}
		s.sessions.rooms = map[string]bool{}
		s.sessions.roots = map[string]bool{}
	}
	if s.turnEnds == nil {
		s.turnEnds = map[string]bool{}
	}
	turnEnded := false
	switch ev.Kind {
	case "posts":
		add := func(address string, ids map[string]string) {
			if address == seatAddr {
				return
			}
			if id := ids[address]; id != "" {
				s.sessions.targets[id] = true
				return
			}
			s.sessions.targets[address] = true
		}
		// The room's root seat takes a post when its stored delivery row wakes
		// it, which room.RootContext wrote from the post, not from its address.
		for _, post := range ev.Posts {
			if post.Wake {
				if seats[post.Message.ID].Mode == room.DeliveryWake {
					s.sessions.roots[ev.RoomID] = true
				}
				for _, address := range post.To {
					add(address, post.SessionIDs)
				}
			}
			// Bound turn-end senders recover queued input and wrap-up work.
			// Unbound senders use the exact IDs from member changes. A typed turn
			// event names the session whose turn ended; an older completion names
			// it in the sender.
			if post.Turn > 0 {
				id := post.SessionIDs[post.From]
				if ev, ok := post.TurnResult(); ok {
					id = ev.SessionID
				}
				if id != "" {
					s.sessions.targets[id] = true
					// A bound session's turn end fires the session-turn-ended
					// hook event, so a job that reacts to it starts at once.
					s.turnEnds[id] = true
					turnEnded = true
				}
			}
		}
	case "members":
		if ev.Change != nil && ev.Change.Op != "delete" && runstore.IsSessionDirID(ev.Change.ID) {
			s.sessions.targets[ev.Change.ID] = true
		}
	case "room.resumed":
		s.sessions.rooms[ev.RoomID] = true
		// A resumed room's check-ins are due again, as a turn end's are: the
		// hooks worker runs one pass, through the hook's own switch.
		s.roomResumed = true
	}
	pending := len(s.sessions.targets) > 0 || len(s.sessions.rooms) > 0 || len(s.sessions.roots) > 0
	// The hooks worker consumes the resume mark with takeRoomResumed.
	resumed := s.roomResumed
	s.mu.Unlock()
	if pending {
		wakeWorker(s.channels["sessions"])
	}
	if resumed {
		wakeWorker(s.channels["hooks"])
	}
	if turnEnded {
		wakeWorker(s.channels["hooks"])
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

// takeTurnEnds returns the bound sessions whose turn ended since the last
// hooks pass.
func (s *workerSignals) takeTurnEnds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.turnEnds))
	for id := range s.turnEnds {
		ids = append(ids, id)
	}
	clear(s.turnEnds)
	sort.Strings(ids)
	return ids
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

// takeRoomResumed reports whether a paused room resumed since the hooks
// worker last ran.
func (s *workerSignals) takeRoomResumed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	resumed := s.roomResumed
	s.roomResumed = false
	return resumed
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
	// wakes them then: instructions sends due instruction notes, fetch fetches
	// local lane remotes, questions retries failed answer deliveries, hooks
	// retries a hook spool drain that could not finish, and prs, merges and
	// sync poll GitHub, merge trials and sync operations while they have work.
	// Parent check-ins run as the parent-checkins built-in hook, not here.
	instructions, fetch, questions, hooks, prs, merges, sync func(context.Context) (time.Time, error)
	// probe reads every enabled machine, and returns when to read them again.
	probe func(context.Context) (time.Time, error)
	// caches cleans the own CI caches of a CI machine once an hour.
	caches func(context.Context) (time.Time, error)
	// settle finishes the hook runs that an earlier engine left running. Only
	// the engine that owns the worker lock runs it, so a second engine on the
	// same store cannot fail the first engine's live runs.
	settle func(context.Context) error
	// hookSchedule is the hook runner's own clock: it finishes install
	// receipts, ticks each repository's schedule and the global built-ins once
	// a minute. It runs under the worker lock with the other workers.
	hookSchedule func(context.Context) (time.Time, error)
	// onStop runs as soon as the workers stop, before they wait for the ticks
	// that are still running. Local CI runs must see their cancellation as
	// early as the engine does, because their teardown is remote work.
	onStop func()
	// stopLocalCI cancels the local CI runs this engine started and waits for
	// their work on the machine to end. A run outlives the worker that started
	// it, and its keeper holds the machine's cache lock, so engine stop ends the
	// runs before the process does.
	stopLocalCI func(context.Context) []string
}

func engineWorkers(engineCtx context.Context, rooms *roomsvc.Service, signals *workerSignals, load *sysload.Sampler, events *hub.Hub, hookService hooks.Service, started time.Time) workers {
	prs := watch.New()
	prs.Notify = rooms.Notify
	prs.GH.Rooms = rooms.Notify.Rooms
	// A local CI run starting or ending changes what a machine runs, so the
	// app reads the machine view again. This is a client notice, not a state
	// write, so no worker wakes and no GitHub read follows it.
	prs.RunningCIChanged = func() {
		if events != nil {
			events.Publish(hub.Event{Topic: machines.Topic, Key: "ci"})
		}
	}
	syncer := lanesync.New()
	spool := hookService
	// globalSchedule lets one global hook pass run at a time. It outlives the
	// pass that schedules it, so the closure below needs the engine context.
	var globalSchedule sync.Mutex
	return workers{
		prs:         prs.Tick,
		onStop:      prs.CancelLocalCI,
		stopLocalCI: prs.StopLocalCI,
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
			// A turn end fires its event before the spool drains, so a job for
			// it starts in this pass and not one interval later.
			ids := signals.takeTurnEnds()
			resumed := signals.takeRoomResumed()
			turnEnds := fireTurnEnds(ctx, rooms.Notify.Sessions.Store, spool, ids)
			// A turn end or a resumed room can make a check-in due at once, and
			// the hook owns the schedule, so one pass runs here, through the
			// same switch.
			var checkins error
			if len(ids) > 0 || resumed {
				_, _, checkins = RunParentCheckIns(ctx, rooms)
			}
			ran, next, err := spool.Drain(ctx, time.Now())
			if sweep {
				signals.found("hook spool", ran)
			}
			return next, errors.Join(turnEnds, checkins, err)
		},
		hookSchedule: func(ctx context.Context) (time.Time, error) {
			errs := []error{hookService.ReconcileInstall(ctx)}
			repos, err := hookService.Repositories(ctx)
			if err != nil {
				errs = append(errs, err)
			}
			for _, repo := range repos {
				if ctx.Err() != nil {
					return time.Time{}, errors.Join(errs...)
				}
				errs = append(errs, hookService.Tick(ctx, repo, time.Now()))
			}
			// A built-in pass can take minutes, so the global schedule runs on
			// its own goroutine instead of holding this worker. The next tick
			// claims nothing until the interval is due again.
			// The engine's own context, not this pass's: the pass deadline must
			// not cancel a hook that the schedule just started.
			scheduleGlobalHooks(engineCtx, &globalSchedule, func(hookCtx context.Context) error {
				return hookService.TickGlobal(hookCtx, time.Now())
			})
			return time.Now().Add(time.Minute), errors.Join(errs...)
		},
		sessions: func(ctx context.Context) error { return deliverSessions(ctx, rooms.Notify.Sessions, signals) },
		merges:   prs.MergeTick,
		sync:     prs.SyncTick,
		probe: func(ctx context.Context) (time.Time, error) {
			return probeMachines(ctx, load, machineDiskAlerter{notify: rooms.Notify})
		},
		caches: func(ctx context.Context) (time.Time, error) {
			return localci.CleanCachesTick(ctx, store.DataDir())
		},
		fetch: func(ctx context.Context) (time.Time, error) { return syncer.Tick(ctx, time.Now()) },
		instructions: func(ctx context.Context) (time.Time, error) {
			return rooms.Notify.Sessions.FlushInstructionNotes(ctx, time.Now())
		},
		settle: func(ctx context.Context) error { return settleHookRuns(ctx, started) },
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

// fireTurnEnds runs the session-turn-ended event for each session whose turn
// just ended. The event names the session's own working directory, and Emit
// resolves it to the repository that holds the hooks for it.
func fireTurnEnds(ctx context.Context, sessions *subagent.Store, h hooks.Service, ids []string) error {
	var errs []error
	for _, id := range ids {
		info, err := sessions.Inspect(id)
		if err != nil {
			continue // a session that is gone has no turn to report
		}
		errs = append(errs, h.Emit(ctx, hooks.Event{Event: hooks.SessionTurnEnded, Repo: info.Dir, Worktree: info.Dir, Session: info.Address(), Room: info.RoomID}))
	}
	return errors.Join(errs...)
}

// A worker pass runs with its own deadline. A pass that hangs would stop that
// worker and every signal it should handle, so a post to an idle session would
// queue with nothing in the log to say why. The tick gets the deadline, so work
// that reads stores or sockets ends at it.
var (
	workerPassTimeout = 2 * time.Minute
	// workerPassWarn is when a pass that finishes is worth a warning.
	workerPassWarn = 15 * time.Second
	// workerNow and workerPassContext let a test drive the pass clock and its
	// deadline without waiting for either.
	workerNow         = time.Now
	workerPassContext = func(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeout(parent, d)
	}
)

// scheduleGlobalHooks starts one global hook pass on the engine's own lifetime,
// not on the pass that scheduled it. A built-in hook pass can take minutes, and
// the worker's pass deadline belongs to the synchronous work only: cancelling it
// here would stop a scheduled hook before it ran.
func scheduleGlobalHooks(engineCtx context.Context, lock *sync.Mutex, tick func(context.Context) error) {
	if !lock.TryLock() {
		return
	}
	go func() {
		defer lock.Unlock()
		if err := tick(engineCtx); err != nil && engineCtx.Err() == nil {
			log.Print(watch.Sanitize("global hooks: " + err.Error()))
		}
	}()
}

// runWorker keeps one worker serial with itself. Other workers never wait for
// this tick, including when a check-in pass is delivering a post. Each pass is
// timed: one that ends at its deadline, or runs past the warning threshold, is
// reported once, so a slow pass is visible instead of silent.
func runWorker(ctx context.Context, name string, signal <-chan struct{}, tick func(context.Context) error, report func(error)) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-signal:
			if ctx.Err() != nil {
				return
			}
			started := workerNow()
			passCtx, cancelPass := workerPassContext(ctx, workerPassTimeout)
			err := tick(passCtx)
			cancelPass()
			elapsed := workerNow().Sub(started)
			if ctx.Err() != nil {
				return
			}
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				report(fmt.Errorf("%s pass ended at its %s deadline; the next signal runs it again", name, workerPassTimeout))
			case err != nil:
				report(err)
			case elapsed > workerPassWarn:
				report(fmt.Errorf("%s pass took %s; a slow pass delays every post it delivers", name, elapsed.Round(time.Millisecond)))
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
			runWorker(ctx, name, signal, tick, func(err error) { report(name, err) })
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
	timed("CI cache cleanup", signals.channels["caches"], timerCaches, w.caches)
	timed("Question delivery", signals.channels["questions"], timerQuestions, w.questions)
	timed("Machine probe", signals.channels["probe"], timerProbe, w.probe)
	start("Dormant session sweep", sweep, w.sweep)
	timed("Hook spool", signals.channels["hooks"], timerHooks, w.hooks)
	timed("Hook schedule", signals.channels["hook-schedule"], timerHookSchedule, w.hookSchedule)
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
		wake("instructions", "prs", "merges", "sync")
		signals.sweep("questions")
		signals.sweep("hooks")
		t.at(timerSafetySweep, t.now().Add(safetySweepEvery), safety)
	}
	daily = func() { wakeWorker(sweep); t.at(timerSweep, t.now().Add(sweepEvery), daily) }
	// Catch writes made while the engine was down.
	signals.recoverSessions()
	wake("instructions", "fetch", "questions", "hooks", "hook-schedule", "prs", "merges", "sync", "probe", "caches")
	wakeWorker(sweep)
	t.at(timerSafetySweep, t.now().Add(safetySweepEvery), safety)
	t.at(timerSweep, t.now().Add(sweepEvery), daily)
	<-ctx.Done()
	if w.onStop != nil {
		w.onStop()
	}
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
	// A run that the previous owner left pending can never finish, so the owner
	// ends it before the workers start new ones.
	if w.settle != nil {
		if err := w.settle(ctx); err != nil {
			report("settle hook runs", err)
		}
	}
	w.run(ctx, signals, t, report)
	return true, nil
}

// settleFromStart ends the hook runs that an earlier engine left pending,
// measured from this engine's start. A run this engine began, even while its
// startup recovery was still running, is newer than that and stays live.
func settleFromStart(ctx context.Context, started time.Time) error {
	return hooks.New().SettleInterrupted(ctx, started)
}

// settleHookRuns is settleFromStart, replaced in a test that checks the cutoff
// the workers pass.
var settleHookRuns = settleFromStart

// localCIStop is how long an engine stop waits for local CI runs to stop
// polling. Their checks run detached on the machines and keep running; the
// restarted engine reattaches. A run still polling then is reported.
const localCIStop = localci.StopWait

func runWorkers(ctx context.Context, rooms *roomsvc.Service, signals *workerSignals, t *timers, load *sysload.Sampler, events *hub.Hub, hookService hooks.Service, started time.Time) {
	report := func(name string, err error) {
		log.Print(watch.Sanitize(name + ": " + err.Error()))
	}
	workerSet := engineWorkers(ctx, rooms, signals, load, events, hookService, started)
	// Cancellation starts with the engine, so the runs' remote teardown overlaps
	// the worker ticks that are still ending. The budget for that teardown starts
	// here too, so the wait below covers the work it waits for.
	stopDeadline := time.Time{}
	cancelled := workerSet.onStop
	workerSet.onStop = func() {
		stopDeadline = time.Now().Add(localCIStop)
		if cancelled != nil {
			cancelled()
		}
	}
	defer func() {
		if stopDeadline.IsZero() {
			stopDeadline = time.Now().Add(localCIStop)
		}
		stopCtx, cancel := context.WithDeadline(context.Background(), stopDeadline)
		defer cancel()
		if workerSet.stopLocalCI == nil {
			return
		}
		if left := workerSet.stopLocalCI(stopCtx); len(left) > 0 {
			log.Print(watch.Sanitize(fmt.Sprintf("engine stop left %d local CI runs working on their machine: %v", len(left), left)))
		}
	}()
	owned, err := runOwned(ctx, store.DataDir(), workerSet, signals, t, report)
	if err != nil {
		log.Print(watch.Sanitize("workers: " + err.Error()))
		return
	}
	if !owned {
		log.Print("workers: another engine owns this store, so this engine runs none")
	}
}
