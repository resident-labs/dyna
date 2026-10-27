package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/workeroutput"
)

// The turn runner's writes to its session record (item 2, g3458). Each is
// one small typed write. A detached runner sends them to the engine through
// an ordered outbox; a runner inside the engine calls the same functions
// directly, so the two paths cannot differ (rule u1).

// RunnerFinish is what a turn's end writes to its session: only fields the
// runner owns.
type RunnerFinish struct {
	Turn           int                         `json:"turn"`
	Usage          workeroutput.Usage          `json:"usage"`
	Baseline       *workeroutput.UsageBaseline `json:"baseline,omitempty"`
	ContextTokens  int64                       `json:"contextTokens,omitempty"`
	RoomCursor     int64                       `json:"roomCursor,omitempty"`
	HarnessSession string                      `json:"harnessSession,omitempty"`
}

func (m *Manager) runnerMeta(id string, fn func(*Meta)) error {
	_, err := m.store().UpdateMeta(id, fn)
	return err
}

func (m *Manager) setRunnerPid(id string, pid int) error {
	meta, err := m.store().UpdateMeta(id, func(mm *Meta) { mm.Pid = pid })
	if err != nil {
		return err
	}
	// A stop can commit before the runner has a pid, or after its first
	// turn read. Registration delivers that durable intent too.
	if turn, err := m.store().ReadTurn(id, meta.Turns); err == nil && turn.StopAsked {
		return m.signalTurn(meta, meta.Turns, syscall.SIGTERM)
	}
	return nil
}

func (m *Manager) setRuntimePin(id string, pin runtime.Pin) error {
	return m.runnerMeta(id, func(mm *Meta) { mm.RuntimePin = &pin })
}

func (m *Manager) setInstructionsStale(id string, stale bool) error {
	return m.runnerMeta(id, func(mm *Meta) { mm.InstructionsStale = stale })
}

func (m *Manager) setHarnessSession(id, sid string) error {
	return m.runnerMeta(id, func(mm *Meta) { mm.HarnessSession = sid })
}

func (m *Manager) setOwnerDMCursor(id string, cursor int64) error {
	return m.runnerMeta(id, func(mm *Meta) { mm.OwnerDMCursor = &cursor })
}

// finishRunnerMeta adds the turn's usage and clears the runner's pid. It is
// the one runner write that is not idempotent, so ctx's request ID is
// recorded in the same transaction: a resend, even after an engine crash
// between the commit and the stored result, adds the usage once.
func (m *Manager) finishRunnerMeta(ctx context.Context, id string, f RunnerFinish) error {
	_, err := m.store().updateMetaContext(ctx, id, func(mm *Meta) {
		mm.Usage.Add(f.Usage)
		mm.UsageBaseline = f.Baseline
		mm.UsageBaselineTurn = f.Turn
		mm.ContextTokens = f.ContextTokens
		mm.LastActivity = time.Now()
		if f.RoomCursor > mm.RoomCursor {
			mm.RoomCursor = f.RoomCursor
		}
		m.pruneClosedPosts(mm)
		if f.HarnessSession != "" {
			mm.HarnessSession = f.HarnessSession
		}
		mm.Pid = 0
	})
	return err
}

// TurnStart is what a turn records before its harness starts: the room
// posts it drained into its prompt, the prompt built from them, and the
// running mark. Running is false only when the turn fails before it runs;
// the drained posts are then still recorded.
type TurnStart struct {
	Turn    int           `json:"turn"`
	Drained []DrainedPost `json:"drained,omitempty"`
	Prompt  string        `json:"prompt,omitempty"`
	Running bool          `json:"running,omitempty"`
}

// DrainedPost is one room post a turn drained, as its steer, and the sender
// the turn now owes a report, if any.
type DrainedPost struct {
	Steer Steer  `json:"steer"`
	Owed  string `json:"owed,omitempty"`
}

// startTurn writes s in one turn write, which records ctx's request ID in
// the same transaction: a resend, even after an engine crash between the
// commit and the stored result, appends the steers once. The event lines
// follow the commit, so a resend that writes nothing appends none.
func (m *Manager) startTurn(ctx context.Context, id string, s TurnStart) error {
	store := m.store()
	// Measure the input boundary as the turn starts. A post after it is input
	// the turn has not read, and a repair uses this boundary so input queued
	// while the turn ran stays fresh.
	var startCutoff *int64
	if s.Running {
		if meta, err := store.ReadMeta(id); err == nil {
			if cutoff, err := m.measureCutoff(ctx, meta.RoomID); err == nil {
				startCutoff = cutoff
			}
		}
	}
	if _, err := store.updateTurnContext(ctx, id, s.Turn, func(t *Turn) {
		for _, d := range s.Drained {
			t.Steering = append(t.Steering, d.Steer)
			t.PendingInputIDs = append(t.PendingInputIDs, d.Steer.ID)
			t.CarriedIDs = appendUnique(t.CarriedIDs, d.Steer.ID)
			t.Owe(d.Owed)
		}
		if s.Prompt != "" {
			t.Prompt = s.Prompt
		}
		if s.Running {
			t.Status, t.Started = TurnRunning, time.Now()
			if startCutoff != nil {
				t.StartCutoff = startCutoff
			}
		}
	}); err != nil {
		return err
	}
	for _, d := range s.Drained {
		store.appendJournal(id, RoomDrainedJournal{Turn: s.Turn, Message: d.Steer.ID})
	}
	if s.Running {
		cause := WakeCause{}
		if saved, err := store.ReadTurn(id, s.Turn); err == nil {
			cause = saved.Wake
		}
		store.appendJournal(id, TurnStartJournal{Turn: s.Turn, Wake: cause})
	}
	return nil
}

// TurnNote is one fact a running turn records about itself. Each field is
// a fact only the turn runner knows; a note sets one of them, and the
// others stay zero. It is not a general turn patch.
type TurnNote struct {
	Turn int `json:"turn"`
	// Profile is the profile, model, harness and dialect the turn runs on.
	Profile *TurnProfile `json:"profile,omitempty"`
	// Accepted means the harness took the held input: its pending posts are
	// delivered.
	Accepted bool `json:"accepted,omitempty"`
	// PendingPrompt is the exact input saved before launch or a room hold.
	PendingPrompt string `json:"pendingPrompt,omitempty"`
	// RoomHeld records that the runner is waiting for its room to resume.
	RoomHeld bool `json:"roomHeld,omitempty"`
	// Dispatched is a live post handed to the harness.
	Dispatched *DrainedPost `json:"dispatched,omitempty"`
	// Delivered is a dispatched post the harness read, matched by post ID.
	// Receipts from older runners omit IDs and use timestamp and text.
	Delivered      *Steer                 `json:"delivered,omitempty"`
	Delivery       *Delivery              `json:"delivery,omitempty"`
	Fallback       *harness.ModelFallback `json:"fallback,omitempty"`
	HarnessSession string                 `json:"harnessSession,omitempty"`
	AccountSwitch  *harness.AccountSwitch `json:"accountSwitch,omitempty"`
	// CompactError, Repinned and Empty are only event lines: a failed
	// compaction, a runtime moved to the installed version, and a woken turn
	// that found no input.
	CompactError string `json:"compactError,omitempty"`
	Repinned     string `json:"repinned,omitempty"`
	Empty        bool   `json:"empty,omitempty"`
}

// TurnProfile is what a turn runs on.
type TurnProfile struct {
	Profile string `json:"profile"`
	Model   string `json:"model,omitempty"`
	Harness string `json:"harness"`
	Dialect string `json:"dialect,omitempty"`
}

// apply sets the note's fact on t, and reports whether it changes the turn
// record at all.
func (n TurnNote) apply(t *Turn) bool {
	switch {
	case n.PendingPrompt != "":
		if t.HeldPrompt != n.PendingPrompt {
			t.HeldAccepted = false
		}
		t.HeldPrompt = n.PendingPrompt
	case n.RoomHeld:
		t.RoomHeld = true
	case n.Profile != nil:
		t.Profile, t.Model, t.Harness, t.OutputDialect = n.Profile.Profile, n.Profile.Model, n.Profile.Harness, n.Profile.Dialect
	case n.Accepted:
		t.Accepted, t.HeldAccepted, t.RoomHeld = true, true, false
		for i := range t.Steering {
			if slices.Contains(t.PendingInputIDs, t.Steering[i].ID) {
				t.Steering[i].Delivered = true
			}
		}
	case n.Dispatched != nil:
		t.Steering = append(t.Steering, n.Dispatched.Steer)
		t.Owe(n.Dispatched.Owed)
	case n.Delivered != nil:
		for i := range t.Steering {
			steer := &t.Steering[i]
			matches := steer.ID != "" && steer.ID == n.Delivered.ID
			// TODO: legacy/shim remove later — older active runners omit receipt IDs.
			if n.Delivered.ID == "" {
				matches = steer.TS == n.Delivered.TS && steer.Message == n.Delivered.Message
			}
			if matches {
				steer.Delivered = true
				if steer.SourceOffset == nil && n.Delivered.SourceOffset != nil {
					offset := *n.Delivered.SourceOffset
					steer.SourceOffset = &offset
				}
			}
		}
	case n.Delivery != nil:
		t.Deliveries = append(t.Deliveries, *n.Delivery)
	case n.Fallback != nil:
		t.Fallbacks = append(t.Fallbacks, *n.Fallback)
	case n.HarnessSession != "":
		t.HarnessSession = n.HarnessSession
	case n.AccountSwitch != nil:
		t.AccountSwitch = n.AccountSwitch
	default:
		return false
	}
	return true
}

// journal is the note's journal line, or nil.
func (n TurnNote) journal() JournalPayload {
	switch {
	case n.Delivery != nil:
		return DeferJournal{Turn: n.Turn, Kind: n.Delivery.Kind, Outcome: n.Delivery.Outcome, WaitedMs: n.Delivery.WaitedMs}
	case n.Delivered != nil:
		return SteerDeliveredJournal{Turn: n.Turn}
	case n.Fallback != nil:
		return ModelFallbackJournal{Turn: n.Turn, From: n.Fallback.From, To: n.Fallback.To, Message: n.Fallback.Reason}
	case n.HarnessSession != "":
		return HarnessSessionJournal{Turn: n.Turn, Session: n.HarnessSession}
	case n.CompactError != "":
		return CompactFailedJournal{Turn: n.Turn, Message: n.CompactError}
	case n.Repinned != "":
		return RuntimeRepinJournal{Message: "The previous private bundle was removed. This session now uses the selected installed version.", Version: n.Repinned}
	case n.Empty:
		return TurnEmptyJournal{Turn: n.Turn}
	}
	return nil
}

// facts counts the facts n sets. A note carries exactly one.
func (n TurnNote) facts() int {
	count := 0
	for _, set := range []bool{n.Profile != nil, n.Accepted, n.PendingPrompt != "", n.RoomHeld, n.Dispatched != nil, n.Delivered != nil, n.Delivery != nil,
		n.Fallback != nil, n.HarnessSession != "", n.AccountSwitch != nil, n.CompactError != "", n.Repinned != "", n.Empty} {
		if set {
			count++
		}
	}
	return count
}

// noteTurn records n in one turn write, which records ctx's request ID in
// the same transaction, and then its event line: a resend after an engine
// crash appends nothing twice. The event-only notes (CompactError, Repinned
// and Empty) write no row, so after such a crash their line may repeat.
// A note with no fact or more than one is refused: it is never a patch.
// recordCompaction appends the compaction Dyna ran itself before this turn
// to the turn's normalized output, so the conversation shows it exactly like
// a harness-reported one. The turn already recorded its start offset, so the
// event falls inside it.
func (m *Manager) recordCompaction(id string, c *harness.Compaction) {
	path, err := m.store().NormalizedPath(id)
	if err != nil {
		log.Printf("session %s: record compaction: %v", id, err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		log.Printf("session %s: record compaction: %v", id, err)
		return
	}
	defer f.Close()
	event := workeroutput.Event{Kind: "status", Name: "compaction", State: c.State, Trigger: "dyna", TokensBefore: c.Before}
	if c.State != "completed" {
		event.Text = c.Reason
	}
	if err := workeroutput.NewNormalizer(nil, f).WriteRecord(workeroutput.Record{Events: []workeroutput.Event{event}}); err != nil {
		log.Printf("session %s: record compaction: %v", id, err)
	}
}

func (m *Manager) noteTurn(ctx context.Context, id string, n TurnNote) error {
	if got := n.facts(); got != 1 {
		return fmt.Errorf("a turn note carries exactly one fact; this one carries %d", got)
	}
	store := m.store()
	if n.apply(&Turn{}) {
		if _, err := store.updateTurnContext(ctx, id, n.Turn, func(t *Turn) { n.apply(t) }); err != nil {
			return err
		}
	}
	// The turn's receipt records what the harness read; the delivery rows say
	// the same, so both agree on how far each delivery got. A note for a session
	// that is gone writes nothing.
	if n.Accepted || n.Delivered != nil {
		if meta, err := store.ReadMeta(id); err != nil {
			if !errors.Is(err, ErrNotFound) {
				log.Printf("delivery note: %v", err)
			}
		} else if turn, err := store.ReadTurn(id, n.Turn); err != nil {
			if !errors.Is(err, ErrNotFound) {
				log.Printf("delivery note: %v", err)
			}
		} else {
			m.markTurnDelivered(ctx, meta, turn)
		}
	}
	// Notice delivery is best effort after commit; a replay returns above.
	if n.AccountSwitch != nil {
		if meta, err := store.ReadMeta(id); err != nil {
			log.Printf("account switch note: %v", err)
		} else {
			m.postAccountSwitch(meta, *n.AccountSwitch)
		}
	}
	if payload := n.journal(); payload != nil {
		store.appendJournal(id, payload)
	}
	return nil
}

// TurnEnd is what a turn's end writes to its turn record: the outcome, the
// usage and the output ranges. Only the runner knows them.
type TurnEnd struct {
	Turn               int                             `json:"turn"`
	Status             string                          `json:"status"`
	Result             json.RawMessage                 `json:"result,omitempty"`
	Error              string                          `json:"error,omitempty"`
	WorkerStderr       string                          `json:"workerStderr,omitempty"`  // the failed worker's stderr tail; detail, never cause text
	FailureReason      providerfailure.Reason          `json:"failureReason,omitempty"` // named by the runner's trusted boundary; Error is display text
	Ended              time.Time                       `json:"ended"`
	InputCutoff        *int64                          `json:"inputCutoff,omitempty"`
	Model              string                          `json:"model,omitempty"`
	Usage              workeroutput.Usage              `json:"usage"`
	UsageNormalization workeroutput.UsageNormalization `json:"usageNormalization,omitempty"`
	HarnessSession     string                          `json:"harnessSession,omitempty"`
	RawEnd             int64                           `json:"rawEnd"`
	NormEnd            int64                           `json:"normEnd"`
	// CutoffPending asks the engine to measure the input cutoff itself because
	// the runner could not. The terminal facts still go to the durable queue,
	// so a transient room-log error cannot drop the completed turn.
	CutoffPending bool `json:"cutoffPending,omitempty"`
	// FallbackCutoff is the runner's last read position: input at or before it
	// was part of this turn. It is used when the runner could not measure the
	// room log, because a later EOF would include input that arrived after the
	// turn ended.
	FallbackCutoff *int64 `json:"fallbackCutoff,omitempty"`
}

// endTurn writes e as the turn's final record in one turn write, which
// records ctx's request ID in the same transaction, and then adds the turn to
// the usage rollup, which counts each turn once (a failed rollup is
// repaired by ReconcileUsage). A replacement whose startup timed out ends
// as that error, whatever the runner reported.
func (m *Manager) endTurn(ctx context.Context, id string, e TurnEnd) error {
	store := m.store()
	// A terminal turn never lands without its input boundary. A pending
	// fallback is the runner's own read position, a stable boundary from the
	// turn; a later EOF would suppress input that arrived afterwards. Otherwise
	// the engine measures here, and a failed measure is retryable so the
	// runner's outbox keeps the request instead of dropping it.
	if e.InputCutoff == nil || e.CutoffPending {
		switch {
		case e.FallbackCutoff != nil:
			e.InputCutoff = e.FallbackCutoff
		default:
			meta, err := store.ReadMeta(id)
			if err != nil {
				return err
			}
			cutoff, err := terminalCutoff(m, meta.RoomID)
			if err != nil {
				return callspec.Retryable(err)
			}
			e.InputCutoff = cutoff
		}
	}
	finished, err := store.updateTurnContext(ctx, id, e.Turn, func(t *Turn) {
		t.Status, t.Error, t.Ended = e.Status, e.Error, e.Ended
		t.FailureReason = e.FailureReason
		t.WorkerStderr = e.WorkerStderr
		if t.StopReason == transferStartupTimeout {
			t.Status, t.Error, t.FailureReason = TurnError, transferStartupTimeout, ""
		}
		t.Result = nil
		if len(e.Result) > 0 {
			t.Result = e.Result
		}
		if e.InputCutoff != nil {
			t.InputCutoff = e.InputCutoff
		}
		if e.Model != "" {
			t.Model = e.Model
		}
		t.RoomHeld, t.HeldPrompt, t.HeldAccepted = false, "", false
		t.PendingInputIDs = nil
		t.Usage, t.UsageNormalization = e.Usage, e.UsageNormalization
		if e.HarnessSession != "" {
			t.HarnessSession = e.HarnessSession
		}
		t.RawEnd, t.NormEnd = e.RawEnd, e.NormEnd
		if !t.Reported {
			t.ReportPending = true
		}
	})
	if err != nil {
		return err
	}
	if finished.Status == TurnError && finished.FailureReason.BlocksProfile() && finished.Profile != "" {
		if err := postCooldownNotice(m.notices(), finished.Profile, finished.Error, finished.Ended); err != nil {
			log.Printf("provider notice: %v", err)
		}
	}
	if meta, err := store.ReadMeta(id); err == nil {
		if err := store.RecordTurnUsage(meta, finished); err != nil {
			log.Printf("usage rollup: %v", err)
		}
	}
	return nil
}

// returnDormant makes the session dormant when the turn that just ended left
// it no responsibilities. The job the turn runs in, when it has one, ends with
// that turn and never counts as unfinished work.
func (m *Manager) returnDormant(ctx context.Context, id string, turn int) error {
	if turn <= 0 {
		return errors.New("return dormant requires a positive turn number")
	}
	ctx, err := withCallerJob(ctx, os.Getenv(shells.JobEnv))
	if err != nil {
		return err
	}
	_, err = m.dormantAfterTurn(ctx, id, turn)
	return err
}

// runnerSession is the session of the caller, which must be that session's
// own process. A call never names its session (item 3).
func runnerSession(ctx context.Context) (string, error) {
	a := authority.Of(ctx)
	// A session acts as Session, or as Root when it holds the room's root
	// seat (SessionActor); each is a session.
	sessionRole := a.Role == authority.Session || a.Role == authority.Root
	if !sessionRole || a.Session == "" {
		// Logged, so a runner the engine could not identify shows in its log.
		log.Printf("runner call refused: the caller acts as %q, not as a session", a.Role)
		return "", errors.New("only a session's own turn runner can write its session record")
	}
	// A runner's request ID starts with its session: the engine keys runner
	// calls by the ID alone, so an ID that names another session is refused
	// here, before anything is written or remembered under it.
	if err := callspec.CheckRunnerRequestID(a.Session, callspec.RequestOf(ctx)); err != nil {
		return "", err
	}
	return a.Session, nil
}

// The engine methods. Each writes the caller's own session.

func (m *Manager) RunnerSetPid(ctx context.Context, pid int) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.setRunnerPid(id, pid)
}

func (m *Manager) RunnerSetRuntimePin(ctx context.Context, pin runtime.Pin) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.setRuntimePin(id, pin)
}

func (m *Manager) RunnerSetInstructionsStale(ctx context.Context, stale bool) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.setInstructionsStale(id, stale)
}

func (m *Manager) RunnerSetHarnessSession(ctx context.Context, sid string) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.setHarnessSession(id, sid)
}

func (m *Manager) RunnerSetDMCursor(ctx context.Context, cursor int64) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.setOwnerDMCursor(id, cursor)
}

func (m *Manager) RunnerFinish(ctx context.Context, f RunnerFinish) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.finishRunnerMeta(ctx, id, f)
}

func (m *Manager) RunnerTurnNote(ctx context.Context, n TurnNote) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.noteTurn(ctx, id, n)
}

func (m *Manager) RunnerTurnEnd(ctx context.Context, e TurnEnd) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.endTurn(ctx, id, e)
}

// RunnerCompleteTurn schedules the caller's durable completion intent. The
// receipt cannot consume the report: recovery reads the pending turn row.
func (m *Manager) RunnerCompleteTurn(ctx context.Context, turn int) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	if err := m.store().PendReport(ctx, id, turn); err != nil {
		return err
	}
	if m.TurnReporter == nil {
		return errors.New("turn completion reporter is unavailable")
	}
	return m.TurnReporter(ctx, m, id, turn)
}

// RunnerReturnDormant makes the caller's session dormant when the turn that
// just ended left it no responsibilities. That body checks the stored turn and
// responsibilities again.
func (m *Manager) RunnerReturnDormant(ctx context.Context, turn int) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.returnDormant(ctx, id, turn)
}

func (m *Manager) RunnerTurnStart(ctx context.Context, s TurnStart) error {
	id, err := runnerSession(ctx)
	if err != nil {
		return err
	}
	return m.startTurn(ctx, id, s)
}

// runnerWrites is one turn runner's way to write its session record: the
// engine through an ordered outbox when the runner is detached, or the
// store directly when it runs inside the engine.
type runnerWrites struct {
	m              *Manager
	id             string
	n              int
	out            *engineclient.Outbox
	waitRetryAfter func(time.Duration) <-chan time.Time
	waitCall       func(context.Context, string, []any, any) error
	terminalMu     sync.Mutex
	terminal       context.Context
	terminalCancel context.CancelFunc
}

// outboxClose bounds terminal writes from the first waited final call.
const outboxClose = 10 * time.Minute

func (m *Manager) runnerWrites(id string, n int) *runnerWrites {
	w := &runnerWrites{m: m, id: id, n: n, waitRetryAfter: time.After}
	if m.Engine != nil {
		w.waitCall = m.Engine.Call
		// Unique to this process: the engine remembers applied request IDs,
		// so a later runner must never reuse this one's.
		prefix := fmt.Sprintf("%s/%d/%d/%d", id, n, os.Getpid(), time.Now().UnixNano())
		w.out = engineclient.NewOutbox(m.Engine, prefix)
		w.out.Spool = func(queued []engineclient.Queued) error { return m.spool(id, queued) }
	}
	return w
}

// send queues a write for the engine, or makes it here with direct. A
// failed write is logged; the turn goes on.
func (w *runnerWrites) send(what, method string, direct func() error, args ...any) {
	if w.out != nil {
		w.out.Send(method, args...)
		return
	}
	if err := direct(); err != nil {
		log.Printf("session %s turn %d: %s: %v", w.id, w.n, what, err)
	}
}

// call is send for a write the runner must know succeeded before it goes on.
func (w *runnerWrites) call(ctx context.Context, method string, direct func() error, args ...any) error {
	return w.callResult(ctx, method, direct, nil, args...)
}

func (w *runnerWrites) callResult(ctx context.Context, method string, direct func() error, result any, args ...any) error {
	if w.out != nil {
		return w.out.CallResult(ctx, result, method, args...)
	}
	return direct()
}

func (w *runnerWrites) pid(pid int) {
	w.send("record the runner pid", "RunnerSetPid", func() error { return w.m.setRunnerPid(w.id, pid) }, pid)
}

// runtimePin waits for its write: the harness must not use a pin that was
// not recorded.
func (w *runnerWrites) runtimePin(ctx context.Context, pin runtime.Pin) error {
	return w.call(ctx, "RunnerSetRuntimePin", func() error { return w.m.setRuntimePin(w.id, pin) }, pin)
}

// start records s with the running mark and waits for it: a turn whose
// record cannot say it runs must not run, or a sender would still see it
// queued while the harness works. When the write fails, for example because
// the turn was stopped meanwhile, the drained posts are still kept.
func (w *runnerWrites) start(ctx context.Context, s TurnStart) error {
	s.Running = true
	err := w.turnStart(ctx, s)
	if err != nil {
		w.keepDrained(ctx, s)
	}
	return err
}

// keepDrained records s's drained posts and prompt without the running mark,
// for a turn that fails before it runs. The posts were claimed in the
// queue, so a turn record must hold them even after the turn was stopped.
func (w *runnerWrites) keepDrained(ctx context.Context, s TurnStart) {
	if len(s.Drained) == 0 && s.Prompt == "" {
		return
	}
	s.Running = false
	if err := w.turnStart(context.WithoutCancel(ctx), s); err != nil {
		log.Printf("session %s turn %d: record drained room posts: %v", w.id, w.n, err)
	}
}

func (w *runnerWrites) turnStart(ctx context.Context, s TurnStart) error {
	return w.call(ctx, "RunnerTurnStart", func() error { return w.m.startTurn(ctx, w.id, s) }, s)
}

// Engine installs can keep the listener starting longer than a call deadline.
// This budget bounds retries; each individual call keeps its own deadline.
const runnerInputRestartWait = 5 * time.Minute

// waitInput observes the existing outbox before starting a separate read.
// Cancellation of this read must never expire or spool that mutation queue.
func (w *runnerWrites) waitInput(ctx context.Context, input InputClaim) (bool, error) {
	waitCtx, cancel := context.WithTimeout(callspec.WithRequest(ctx, ""), runnerInputRestartWait)
	defer cancel()
	if err := w.out.WaitIdle(waitCtx); err != nil {
		return false, err
	}
	for delay := 100 * time.Millisecond; ; delay = min(2*delay, time.Second) {
		var ready bool
		err := w.waitCall(waitCtx, "RunnerWaitInput", []any{input}, &ready)
		if err == nil {
			return ready, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if waitCtx.Err() != nil {
			return false, callspec.Late("RunnerWaitInput", runnerInputRestartWait)
		}
		var remote *engineclient.RemoteError
		if errors.As(err, &remote) && remote.Final() && !remoteCanceled(remote) {
			return false, err
		}
		// This is an idempotent read, outside mutation order. A transient
		// transport or 5xx failure must not stop the running native process.
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, callspec.Late("RunnerWaitInput", runnerInputRestartWait)
		case <-w.waitRetryAfter(delay):
		}
	}
}

// remoteCanceled reports an engine reply that its own call was canceled, as when
// the engine stops or restarts under a running wait. The reply says nothing about
// this runner's turn, which stays current: the wait must retry, not fail the
// turn with a steering error (D-267). A revoked turn still fails as a 403.
// TODO: legacy/shim remove later — an engine from before D-269 sends this as a
// 4xx with no code; a current engine sends a 503 engine_stopping, which is never
// final. Remove when no such engine can still be running.
func remoteCanceled(remote *engineclient.RemoteError) bool {
	return strings.Contains(remote.Message, context.Canceled.Error()) || strings.Contains(remote.Message, context.DeadlineExceeded.Error())
}

func (w *runnerWrites) claimInput(ctx context.Context, input InputClaim) (ClaimedInput, error) {
	var reply ClaimedInput
	err := w.callResult(ctx, "RunnerClaimInput", func() error {
		var err error
		reply, err = w.m.claimInput(ctx, w.id, input)
		return err
	}, &reply, input)
	return reply, err
}

// turnEnd writes the turn's final record and waits for it: the runner reads
// the record it ended with for the report that follows.
func (w *runnerWrites) turnEnd(ctx context.Context, e TurnEnd) error {
	w.terminalContext()
	return w.call(ctx, "RunnerTurnEnd", func() error { return w.m.endTurn(ctx, w.id, e) }, e)
}

func (w *runnerWrites) returnDormant(ctx context.Context) error {
	return w.call(ctx, "RunnerReturnDormant", func() error { return w.m.returnDormant(ctx, w.id, w.n) }, w.n)
}

// note records a fact about this turn.
func (w *runnerWrites) note(n TurnNote) {
	n.Turn = w.n
	w.send("record turn detail", "RunnerTurnNote", func() error { return w.m.noteTurn(context.Background(), w.id, n) }, n)
}

// noteWait is note for a fact the runner must know was recorded.
func (w *runnerWrites) noteWait(ctx context.Context, n TurnNote) error {
	n.Turn = w.n
	return w.call(ctx, "RunnerTurnNote", func() error { return w.m.noteTurn(ctx, w.id, n) }, n)
}

func (w *runnerWrites) instructionsStale(stale bool) {
	w.send("mark instructions stale", "RunnerSetInstructionsStale", func() error { return w.m.setInstructionsStale(w.id, stale) }, stale)
}

func (w *runnerWrites) harnessSession(sid string) {
	w.send("record the harness session", "RunnerSetHarnessSession", func() error { return w.m.setHarnessSession(w.id, sid) }, sid)
}

func (w *runnerWrites) dmCursor(cursor int64) {
	w.send("record the DM cursor", "RunnerSetDMCursor", func() error { return w.m.setOwnerDMCursor(w.id, cursor) }, cursor)
}

func (w *runnerWrites) finish(f RunnerFinish) {
	w.send("record the turn usage", "RunnerFinish", func() error { return w.m.finishRunnerMeta(context.Background(), w.id, f) }, f)
}

// flush settles every earlier write before the runner releases its lock.
func (w *runnerWrites) flush(ctx context.Context) error {
	if w.out == nil {
		return nil
	}
	return w.out.Flush(ctx)
}

// complete queues the report only after the runner releases its turn lock.
// The engine already has its pending intent in the terminal turn row.
func (w *runnerWrites) complete() {
	local := func() error {
		if w.m.TurnReporter != nil {
			return w.m.TurnReporter(w.terminalContext(), w.m, w.id, w.n)
		}
		return nil
	}
	w.send("report the completed turn", "RunnerCompleteTurn", local, w.n)
}

// close settles the completion notification after the turn lock is released,
// using the same terminal deadline as the final writes and flush. It is
// safe to call more than once.
func (w *runnerWrites) close(ctx context.Context) {
	if w.out != nil {
		w.terminalContext()
		w.out.Close(ctx)
	}
	w.terminalMu.Lock()
	if w.terminalCancel != nil {
		w.terminalCancel()
	}
	w.terminalMu.Unlock()
}
