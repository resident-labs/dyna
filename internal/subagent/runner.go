package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/structuredoutput"
	"dyna-agent/internal/textutil"
	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workerprompt"
	"dyna-agent/internal/worktrees"
)

// EffectiveDir returns the working directory for a turn plus an optional prompt
// note. When a session's saved directory no longer exists, for example after it
// removes its own worktree, the turn runs in the room's repo checkout and the
// note tells the model why. An empty note means the saved directory is intact.
func (m *Manager) EffectiveDir(meta Meta) (dir, note string) {
	if info, err := os.Stat(meta.Dir); err == nil && info.IsDir() {
		return meta.Dir, ""
	}
	missing := meta.Dir
	if missing == "" {
		missing = "(none)"
	}
	fallback := ""
	if rec, err := m.rooms().Resolve(meta.RoomID); err == nil {
		if info, err := os.Stat(rec.Repo); rec.Repo != "" && err == nil && info.IsDir() {
			fallback = rec.Repo
		}
	}
	if fallback == "" {
		return meta.Dir, ""
	}
	note = fmt.Sprintf("[DYNA WORKING DIRECTORY] Your saved working directory %s no longer exists. This turn runs in the room's repo checkout %s instead. Choose a task worktree before editing code.", missing, fallback)
	return fallback, note
}

// RunTurn executes one turn of a session to completion and records its result.
// It owns lock (the session's turn lock) until final writes finish, then
// releases it before publishing completion. There is no
// default deadline for ordinary sessions. A stored deadline bounds the turn
// across retries and continuations. Otherwise it ends when ctx is canceled
// (dyna subagent stop / close, or a signal to the runner).
func (m *Manager) RunTurn(ctx context.Context, id string, n int, lock *os.File) error {
	syscall.CloseOnExec(int(lock.Fd()))
	defer lock.Close()
	store := m.store()
	writes := m.runnerWrites(id, n)
	// Deferred after the lock's Close, so it runs first: queued writes are
	// sent before the turn lock is released.
	defer writes.close(context.WithoutCancel(ctx))
	// Register before reading the stop intent, so a concurrent stop either
	// cancels this context or is seen by the first turn read.
	ctx, cancelTurn := m.turnContext(ctx, id, n)
	defer cancelTurn()
	meta, err := store.ReadMeta(id)
	if err != nil {
		return err
	}
	turn, err := store.ReadTurn(id, n)
	if err != nil {
		return err
	}
	if turn.StopAsked {
		cancelTurn()
	}
	if !turn.Deadline.IsZero() {
		bounded, cancel := context.WithDeadline(ctx, turn.Deadline)
		defer cancel()
		ctx = bounded
	}
	writes.pid(os.Getpid())
	roomCursorNext := meta.RoomCursor

	var (
		usage         workeroutput.Usage
		normalization workeroutput.UsageNormalization
		baseline      = meta.UsageBaseline
		contextTokens = meta.ContextTokens
		sessionID     = meta.HarnessSession
		actualModel   string
		mu            sync.Mutex
		gate          *turnGate
	)
	if meta.UsageBaselineTurn != n-1 {
		baseline = nil
	}
	finish := func(status string, result any, runErr error) error {
		finishCtx := writes.terminalContext()
		rawPath, _ := store.RawOutputPath(id)
		normPath, _ := store.NormalizedPath(id)
		mu.Lock()
		turnUsage, sid, model := usage, sessionID, actualModel
		turnNormalization, turnBaseline, turnContext := normalization, baseline, contextTokens
		mu.Unlock()
		msg := ""
		if runErr != nil {
			msg = runErr.Error()
		}
		// The reason comes from a trusted boundary deeper in the error chain, so
		// the engine routes recovery by it rather than by this display text.
		failureReason, _ := providerfailure.ReasonOf(runErr)
		ended := time.Now()
		// Settle the live gate first: its final read cursor is this turn's input
		// boundary, including steering it claimed even when delivery failed. A
		// failed measurement must not drop the completed turn, so that cursor is
		// also the fallback boundary, never a later EOF.
		cursor := roomCursorNext
		fallback := cursor
		if gate != nil {
			// The boundary covers a live claim the engine committed even if the
			// harness never took its reply, but the gate keeps its published
			// cursor so the next turn recovers that post.
			fallback = gate.Settle(finishCtx)
			gate.Deactivate()
			if at, ok := store.closedAt(id, n); ok {
				cursor = at
			}
			if cursor > fallback {
				fallback = cursor
			}
		}
		cutoff, cutoffErr := m.measureCutoff(finishCtx, meta.RoomID)
		if cutoffErr != nil {
			log.Printf("session %s turn %d: %v; the terminal write uses the read position", id, n, cutoffErr)
		}
		end := TurnEnd{Turn: n, Status: status, Error: msg, Ended: ended, Model: model, Usage: turnUsage,
			FailureReason:      failureReason,
			WorkerStderr:       harness.StderrDetail(runErr),
			UsageNormalization: turnNormalization, HarnessSession: sid, RawEnd: fileSize(rawPath), NormEnd: fileSize(normPath),
			InputCutoff: cutoff, CutoffPending: cutoff == nil, FallbackCutoff: &fallback}
		if result != nil {
			if b, err := json.Marshal(result); err == nil {
				end.Result = b
			}
		}
		werr := writes.turnEnd(finishCtx, end)
		if werr == nil {
			status, msg = endedAs(func() (Turn, error) { return store.ReadTurn(id, n) }, status, msg)
		}
		writes.finish(RunnerFinish{Turn: n, Usage: turnUsage, Baseline: turnBaseline, ContextTokens: turnContext, RoomCursor: cursor, HarnessSession: sid})
		ev := TurnEndJournal{Turn: n, Status: status, Error: msg}
		if werr != nil {
			// The event is appended either way; say when the turn row
			// was not written, so a later repair is explained.
			ev.WriteError = werr.Error()
		}
		if s, ok := result.(string); ok && s != "" {
			ev.Preview = textutil.Prefix(s, 240)
		} else if result != nil {
			if b, err := json.Marshal(result); err == nil {
				ev.Preview = textutil.Prefix(string(b), 240)
			}
		}
		if m.checkpoint != nil {
			m.checkpoint("turn-finished")
		}
		// A turn that ends with no work left makes the session dormant, while
		// this runner still owns the turn lock.
		if werr == nil {
			cleanupCtx, stop := context.WithTimeout(finishCtx, 5*time.Second)
			if err := writes.returnDormant(cleanupCtx); err != nil {
				log.Printf("return dormant session %s: %v", id, err)
			}
			stop()
		}
		// Settle final writes before releasing the turn lock. Completion and
		// member events must see an idle session before they retry pending input.
		flushErr := writes.flush(finishCtx)
		_ = lock.Close()
		// Named item 2 exception: an append-only log, written after the
		// lock so that completion sees an idle session.
		store.appendJournal(id, ev)
		if werr == nil || errors.Is(werr, engineclient.ErrSpooled) {
			writes.complete()
		}
		writes.close(finishCtx)
		return errors.Join(werr, flushErr)
	}
	fail := func(err error) error {
		status := TurnError
		if ctx.Err() != nil {
			status = TurnStopped
		}
		_ = finish(status, nil, err)
		return err
	}

	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	profs, err := m.profiles()
	if err != nil {
		return fail(err)
	}
	p, ok := profs.Get(meta.Profile)
	if meta.Run != nil && meta.Run.Profile != nil {
		p, ok = meta.Run.Profile, true
	}
	if !ok {
		return fail(fmt.Errorf("profile %q is no longer registered", meta.Profile))
	}
	prof := *p
	if meta.Model != "" {
		models := prof.EffectiveModels()
		if len(models) <= 1 || !slices.Contains(models, meta.Model) {
			prof.Model = meta.Model
			prof.Models = nil
		} else {
			rest := slices.DeleteFunc(slices.Clone(models), func(m string) bool { return m == meta.Model })
			prof.Model, prof.Models = meta.Model, append([]string{meta.Model}, rest...)
		}
	}
	writes.note(TurnNote{Profile: &TurnProfile{Profile: prof.Name, Model: prof.Model, Harness: prof.Harness, Dialect: prof.EffectiveDialect()}})
	prof.Env = cloneEnv(prof.Env)
	childenv.SetTrimpath(prof.Env)
	scratchPath, err := scratch.Ensure(id)
	if err != nil {
		log.Printf("session scratch unavailable: %v", err)
	}
	prof.Env[scratch.Env] = scratchPath
	if err := scratch.SetTemp(prof.Env, id); err != nil {
		log.Printf("session temporary folder unavailable: %v", err)
	}
	stampSession(prof.Env, meta)
	prof.Env[runstore.SessionEnv] = id
	if exe := m.executable(); exe != "" {
		prof.Env["DYNA_BIN"] = exe
	}

	rawPath, _ := store.RawOutputPath(id)
	normPath, _ := store.NormalizedPath(id)
	settingsValues := m.settings()
	// Sessions keep no work journal: progress shows in the live transcript
	// and in room posts. An empty journal Path turns off journal supervision
	// (no idle or completion reminders) while raw and normalized output are
	// still captured.
	outputOpts := harness.JournalOptions{
		OutputPath: rawPath, NormalizedOutputPath: normPath,
		Output:            prof.EffectiveOutput(),
		CompletionTimeout: -1, // subagent turns have no time limits of any kind
		MaxToolWait:       settingsValues.SteeringMaxToolWait,
		OnDelivery: func(note harness.DeliveryNote) {
			d := Delivery{TS: time.Now().UnixMilli(), Kind: note.Kind, Outcome: note.Outcome, Reason: note.Reason, WaitedMs: note.Waited.Milliseconds()}
			writes.note(TurnNote{Delivery: &d})
		},
	}
	// A run agent keeps the run-owned work journal, which also turns on the
	// harness's idle reminder for it.
	if run := meta.Run; run != nil && run.Journal != "" {
		outputOpts.Path = run.Journal
		outputOpts.IdleAfter = harness.JournalIdleFromEnv(runJournalIdle)
	}
	steeringOpts := harness.SteeringOptions{
		StartGate: func(ctx context.Context, prompt string, launch func() error) error {
			for {
				if err := writes.waitSession(ctx, prompt); err != nil {
					return err
				}
				err := m.roomExecutionGate(ctx, meta.RoomID, func() error {
					if !writes.resumeActive() {
						return context.DeadlineExceeded
					}
					return launch()
				})
				if errors.Is(err, errRoomPaused) {
					continue
				}
				return err
			}
		},
		DeliveryGate: func(deliver func() error) error { return m.roomExecutionGate(ctx, meta.RoomID, deliver) },
		Paused:       func() bool { return m.RoomPaused(meta.RoomID) },
		BeforeStart:  func(ctx context.Context, prompt string) error { return writes.waitSession(ctx, prompt) },
		OnStart: func() {
			writes.note(TurnNote{Accepted: true})
			if m.checkpoint != nil {
				m.checkpoint("turn-started")
			}
		},
		OnDispatch: func(msg runstore.SteeringMessage) error {
			if !writes.resumeActive() {
				return context.DeadlineExceeded
			}
			post := room.Message{From: msg.From}
			if msg.ID != "" {
				if saved, err := m.rooms().Message(meta.RoomID, msg.ID); err == nil {
					post = saved
				}
			}
			d := DrainedPost{Steer: Steer{ID: msg.ID, TS: msg.TS, Message: msg.Message}}
			if store.Owes(meta, post) {
				d.Owed = msg.From
			}
			return writes.noteWait(ctx, TurnNote{Dispatched: &d})
		},
		OnMessage: func(msg runstore.SteeringMessage, sourceOffset *int64) {
			writes.note(TurnNote{Delivered: &Steer{ID: msg.ID, TS: msg.TS, Message: msg.Message, SourceOffset: sourceOffset}})
		},
	}

	if err := writes.waitSession(ctx, turn.HeldPrompt); err != nil {
		return fail(err)
	}
	resumable := meta.Resumable
	// The instructions stay byte for byte the same for the life of a harness
	// conversation, so its cached prompt prefix holds. They refresh only where
	// that cache is already lost; otherwise a change note reports the drift.
	beforeInstructionsRefresh()
	instructionState, err := writes.instructions(ctx)
	if err != nil {
		return fail(err)
	}
	frozen, covered, refreshed := instructionState.Frozen, instructionState.Covered, instructionState.Refreshed
	prof.RuntimeInstructions = frozen.Text()
	// A session can remove its own worktree. Its saved directory then no longer
	// exists, so the turn runs in the room's repo checkout and the prompt says so.
	runDir, dirNote := m.EffectiveDir(meta)
	// A Pi session opened from another directory asks whether to fork on stdin,
	// which a headless turn cannot answer. If the session's own directory still
	// exists, the resume opens the session file and Pi keeps that directory. If
	// it is gone, fork the session into this turn's directory with a new id.
	forkFrom := ""
	forkPending := false
	if prof.Harness == profile.HarnessPi && sessionID != "" {
		if path, err := runtime.PiSessionPath(sessionID); err == nil {
			if cwd, err := runtime.PiSessionCwd(path); err == nil {
				if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
					forkFrom, forkPending = path, true
				}
			}
		}
	}
	maps.Copy(prof.Env, worktrees.PortEnvForDir(ctx, runDir))
	runOne := func(ask string) (harness.Result, error) {
		if !writes.resumeActive() {
			return harness.Result{}, context.DeadlineExceeded
		}
		// The note is added at this shared boundary so every prompt shape and
		// every schema retry carries it, including prompts rebuilt from base.
		if dirNote != "" {
			ask = dirNote + "\n\n" + ask
		}
		if err := ctx.Err(); err != nil {
			return harness.Result{}, err
		}
		if err := writes.waitSession(ctx, ask); err != nil {
			return harness.Result{}, err
		}
		mu.Lock()
		resumeID, lastContext := sessionID, contextTokens
		forking := forkPending && forkFrom != ""
		mu.Unlock()
		execution := writes.nativeExecution(meta.Role)
		execution.ProcessRunner = m.ProcessRunner
		if turn.Schema == "" {
			execution.AcceptEmptyOutput = func() bool { return m.turnAcceptsEmptyOutput(id, n) }
			execution.AcceptCleanEmptyStop = true
		}
		sessionOpts := harness.SessionOptions{
			RecoveryDelay: m.RecoveryDelay,
			ResumeID:      resumeID,
			RuntimePin:    meta.RuntimePin,
			OnRuntime: func(pin runtime.Pin) error {
				if meta.RuntimePin != nil && meta.RuntimePin.Bundle.Version != pin.Bundle.Version {
					writes.note(TurnNote{Repinned: pin.Bundle.Version})
				}
				meta.RuntimePin = &pin
				return writes.runtimePin(ctx, pin)
			},
			UsageBaseline: baseline,
			ContextTokens: lastContext,
			OnFallback: func(f harness.ModelFallback) {
				writes.note(TurnNote{Fallback: &f})
			},
			OnCompaction: func() {
				writes.instructionsStale(true)
			},
			OnSession: func(sid string) {
				mu.Lock()
				sessionID = sid
				forkPending = false
				mu.Unlock()
				writes.harnessSession(sid)
				writes.note(TurnNote{HarnessSession: sid})
			},
		}
		if forking {
			sessionOpts.ResumeID = ""
			sessionOpts.ForkFrom = forkFrom
		}
		r, err := harness.RunWithOptions(ctx, prof, ask, runDir, true, outputOpts, steeringOpts, sessionOpts, execution)
		if r.CompactError != "" {
			writes.note(TurnNote{CompactError: r.CompactError})
		}
		if r.Compaction != nil {
			m.recordCompaction(id, r.Compaction)
		}
		mu.Lock()
		if r.ContextTokens > 0 {
			contextTokens = r.ContextTokens
		}
		if r.Model != "" {
			actualModel = r.Model
		}
		usage.Add(r.Usage)
		normalization.Merge(r.UsageNormalization)
		baseline = r.UsageBaseline
		if r.SessionID != "" {
			sessionID = r.SessionID
		}
		mu.Unlock()
		if r.AccountSwitch != nil {
			writes.note(TurnNote{AccountSwitch: r.AccountSwitch})
		}
		return r, err
	}

	base := turn.Prompt
	roomContext := store.SelectorContext(meta)
	if usage := usageFrame(prof.Harness); usage != "" {
		roomContext += "\n" + usage
	}
	// The instructions carry the whole spawn goal, so the goal frame leaves
	// out its clipped line.
	shownGoal := ""
	goalBlock, hasBrief := m.spawnGoalBlock(meta)
	if goalBlock != "" {
		shownGoal = meta.Goal
	}
	if frame := m.goalFrame(meta, shownGoal); frame != "" {
		roomContext += "\n" + frame
	}
	if n == 1 || turn.TransferID != "" {
		// A new session's brief is in the instructions. A transfer packet or a
		// service assignment still follows it.
		task := turn.Prompt
		if n == 1 && turn.TransferID == "" && hasBrief {
			task = ""
		}
		base = firstTurnPrompt(roomContext, task)
	} else {
		base = followUpPrompt(n, turn.Prompt, roomContext)
	}
	claimed, err := writes.claimInput(ctx, InputClaim{Turn: n, Startup: true, Covered: covered})
	if err != nil {
		return fail(err)
	}
	nextCursor := claimed.Cursor
	roomCursorNext = nextCursor
	// Input is returned only after the engine commits its claims and result.
	start := TurnStart{Turn: n}
	var frames []string
	for _, post := range claimed.Posts {
		frames = append(frames, post.Steer.Message)
		start.Drained = append(start.Drained, DrainedPost{Steer: post.Steer, Owed: post.Owed})
	}
	// The internal root never wakes for the owner's DMs to agents or for quiet
	// service reports, but it reads a short list of them with the input
	// of its next turn that has posts. Until then the DM watermark stays put.
	if meta.Role == RoleRoot {
		from := meta.RoomCursor
		if meta.OwnerDMCursor != nil {
			from = *meta.OwnerDMCursor
		}
		if len(frames) > 0 {
			if msgs, end, err := m.rooms().ReadAll(meta.RoomID, from); err == nil {
				frames = append(frames, seatNotes(m.rooms(), meta, msgs)...)
				from = end
			}
		}
		writes.dmCursor(from)
	}
	if n > 1 && strings.TrimSpace(turn.Prompt) == "" && turn.HeldPrompt == "" {
		// A turn woken by posts: they are its input.
		if len(frames) == 0 {
			writes.note(TurnNote{Empty: true})
			return finish(TurnOK, nil, nil)
		}
		turn.Prompt = strings.Join(frames, "\n\n")
		start.Prompt = turn.Prompt
		base = followUpPrompt(n, turn.Prompt, roomContext)
	} else if len(frames) > 0 {
		base = workerprompt.AppendPromptSuffix(base, "\n\n"+strings.Join(frames, "\n\n"))
	}
	if refreshed {
		// A harness without a native channel reads the instructions in the
		// prompt of each turn that takes a new snapshot.
		base = harness.WithPromptInstructions(prof, base)
	}
	gate = store.newTurnGate(m.rooms(), meta, n, nextCursor)
	gate.writes, gate.ctx = writes, ctx
	if meta.Resumable {
		if err := gate.Activate(); err != nil {
			writes.keepDrained(ctx, start)
			return fail(err)
		}
	}
	steeringOpts.Source = gate
	ask := withSchema(base, turn.Schema)
	if turn.HeldPrompt != "" {
		ask = turn.HeldPrompt
		if turn.HeldAccepted && meta.HarnessSession != "" {
			ask = "Continue the pending work in this exact session. The previous runner ended before recording the result. Do not repeat completed actions."
		}
	}

	// Running means startup has finished draining the prompt and the live
	// steering source owns all newer posts. Publishing it earlier lets a
	// sender mistake a prompt-bound post for live steering before the harness
	// has even started. Queued posts still drain exactly once above.
	// A turn whose record cannot say it runs must not run: a sender would
	// still see it queued while the harness worked.
	if err := writes.start(ctx, start); err != nil {
		return fail(fmt.Errorf("mark turn %d running: %w", n, err))
	}

	if turn.Schema == "" {
		r, err := runOne(ask)
		if err != nil {
			// Record the failed turn first, so the fallback coordinator sees a
			// finished turn and never repeats it.
			failure := fail(err)
			m.fallbackAfterLimit(ctx, meta, err)
			return failure
		}
		return finish(TurnOK, r.Output, nil)
	}
	compiled, err := structuredoutput.Compile(turn.Schema)
	if err != nil {
		return fail(err)
	}
	var lastErr error
	var raw string
	for attempt := 0; attempt < structuredoutput.SchemaAttempts; attempt++ {
		r, err := runOne(ask)
		if err != nil {
			failure := fail(err)
			m.fallbackAfterLimit(ctx, meta, err)
			return failure
		}
		raw = r.Output
		val, jerr := compiled.Validate(raw)
		if jerr == nil {
			return finish(TurnOK, val, nil)
		}
		lastErr = jerr
		mu.Lock()
		canResume := resumable && sessionID != ""
		mu.Unlock()
		if canResume {
			ask = structuredoutput.SchemaCorrection(jerr, raw, false) + structuredoutput.SchemaSuffix(turn.Schema)
		} else {
			ask = workerprompt.AppendPromptSuffix(withSchema(base, turn.Schema), "\n\n"+structuredoutput.SchemaCorrection(jerr, raw, true))
		}
	}
	err = fmt.Errorf("schema validation failed after %d attempts: %w", structuredoutput.SchemaAttempts, lastErr)
	_ = finish(TurnError, raw, err)
	return err
}

// drainRoomMessages returns the posts to this session after its room cursor
// and the next cursor. It skips posts the previous turn already delivered:
// a turn whose runner died never advanced the cursor.
func (m *Manager) drainRoomMessages(meta Meta, current Turn) ([]room.Message, int64, error) {
	msgs, next, err := m.rooms().ReadAll(meta.RoomID, meta.RoomCursor)
	if err != nil {
		return nil, meta.RoomCursor, err
	}
	claims, err := sessionClaims(m.rooms(), meta)
	if err != nil {
		return nil, meta.RoomCursor, err
	}
	// Unrelated room traffic needs no previous-turn receipt reads.
	input, err := forSession(m.rooms(), meta, msgs)
	if err != nil {
		return nil, meta.RoomCursor, err
	}
	if len(input) == 0 && len(claims.claimedTurn) == 0 && len(meta.InheritedInboxes) == 0 {
		return nil, next, nil
	}
	// One read covers the previous turn and every claimed turn.
	turns := current.N - 1
	for _, n := range claims.claimedTurn {
		turns = max(turns, n)
	}
	steers, err := m.store().steering(meta.ID, turns)
	if err != nil {
		return nil, meta.RoomCursor, err
	}
	type turnPost struct {
		turn int
		id   string
	}
	// delivered names posts a turn already took. A claimed post whose cursor
	// was not committed stays in the room-log result read above, so the
	// selection below must skip it there too.
	delivered := map[string]bool{}
	received := map[turnPost]bool{}
	for _, st := range steers {
		if !st.Delivered {
			continue
		}
		delivered[st.ID] = true
		received[turnPost{st.Turn, st.ID}] = true
	}
	var out []room.Message
	for messageID, n := range claims.claimedTurn {
		if received[turnPost{n, messageID}] || claims.cancelled[messageID] || claims.dismissed[messageID] || slices.Contains(meta.UncertainInputIDs, messageID) {
			continue
		}
		// An accepted turn carried the claimed post in its prompt even when no
		// steering record names it; it must not enter another turn. An
		// unreadable claimed turn fails closed: redelivering could repeat a
		// post the turn already read.
		claimed, err := m.store().ReadTurn(meta.ID, n)
		if err != nil {
			return nil, meta.RoomCursor, err
		}
		if claimed.DeliveredPost(messageID) {
			delivered[messageID] = true
			continue
		}
		msg, e := m.rooms().Message(meta.RoomID, messageID)
		if e != nil {
			return nil, meta.RoomCursor, e
		}
		msgs = append(msgs, msg)
	}
	// Claims recovered from an older cursor precede later posts. Preserve log
	// order even when message timestamps tie and the claim map iterates randomly.
	if len(claims.claimedTurn) > 0 {
		if err := m.rooms().OrderMessages(meta.RoomID, msgs); err != nil {
			return nil, meta.RoomCursor, err
		}
	}
	// The recovered claims are input too, so they are selected with the posts
	// read from the room log.
	if input, err = forSession(m.rooms(), meta, msgs); err != nil {
		return nil, meta.RoomCursor, err
	}
	selected, err := m.store().inboxMessages(m.rooms(), meta, input)
	if err != nil {
		return nil, meta.RoomCursor, err
	}
	for _, msg := range selected {
		if claims.cancelled[msg.ID] && len(meta.InheritedInboxes) == 0 {
			continue
		}
		if delivered[msg.ID] || claims.dismissed[msg.ID] || slices.Contains(meta.UncertainInputIDs, msg.ID) {
			continue
		}
		delivered[msg.ID] = true
		out = append(out, msg)
	}
	return out, next, nil
}

// stampSession writes the Dyna identity stamp of a session's worker. The
// internal root acts as the room owner: root mode, with DYNA_SESSION for
// attribution only (see mode.CurrentSession). Empty values override stamps the
// spawning process may carry.
// runJournalIdle is how long a run agent may go without a journal entry before
// the harness sends a reminder.
const runJournalIdle = 20 * time.Minute

func stampSession(env map[string]string, meta Meta) {
	env[mode.EnvMode] = mode.Subagent
	env[mode.EnvSession] = meta.ID
	env[mode.EnvDepth] = strconv.Itoa(meta.Depth)
	env[mode.EnvRoomID] = meta.RoomID
	env[mode.EnvRoom] = meta.Room
	env[mode.EnvAddress] = meta.Address()
	// A run agent's processes carry the run, so cancelling the run finds them.
	if run := meta.Run; run != nil {
		env[mode.EnvWorkerRun] = run.ID
		if run.Journal != "" {
			env[runstore.AgentJournalEnv] = run.Journal
			env[runstore.AgentJournalRootEnv] = run.JournalRoot
		}
	}
	if meta.Role == RoleRoot {
		env[mode.EnvMode] = mode.Root
		env[mode.EnvDepth] = ""
		env[mode.EnvAddress] = ""
	}
}

// StampEnv is stampSession as KEY=value pairs, for commands that run for a
// session outside its worker, such as shell jobs.
func StampEnv(meta Meta) []string {
	env := map[string]string{}
	stampSession(env, meta)
	// A shared service has no scratch directory for this session in its own environment.
	if path, err := scratch.Ensure(meta.ID); err == nil {
		env[scratch.Env] = path
	}
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

func cloneEnv(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src)+8)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// endedAs is what a turn whose end was written ended as: its record's status
// and error, which may say more than the runner sent, such as a
// replacement's startup timeout. When the record cannot be read, it is the
// runner's own status and error. That read failure is only logged: the end
// was written, so it is not a write error and does not skip the turn's
// finish.
func endedAs(read func() (Turn, error), status, msg string) (string, string) {
	t, err := read()
	if err != nil {
		log.Printf("read the ended turn: %v", err)
		return status, msg
	}
	return t.Status, t.Error
}
