package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionconv"
)

// TransferState is durable state for a fresh model conversation in the same
// Dyna session. The session's address, children and PR links never move.
const transferStartupTimeout = "replacement startup produced no model output within 60 seconds"

type TransferState struct {
	// RootSeat collects a handoff for an external root without a replacement turn.
	RootSeat          bool          `json:"rootSeat,omitempty"`
	PausedFor         time.Duration `json:"pausedFor,omitempty"`
	WaitFor           time.Duration `json:"waitFor,omitempty"`
	ID                string        `json:"id"`
	Session           string        `json:"session"`
	Phase             string        `json:"phase"` // waiting, stopping, starting, completed, failed, cancelled
	Profile           string        `json:"profile"`
	Model             string        `json:"model,omitempty"`
	PreviousProfile   string        `json:"previousProfile"`
	PreviousModel     string        `json:"previousModel,omitempty"`
	PreviousHarness   string        `json:"previousHarness,omitempty"`
	PreviousDir       string        `json:"previousDir"`
	PreviousResumable bool          `json:"previousResumable"`
	OldTurns          int           `json:"oldTurns"`
	Requested         time.Time     `json:"requested"`
	Deadline          time.Time     `json:"deadline"`
	StartupDeadline   time.Time     `json:"startupDeadline,omitempty"`
	Ended             time.Time     `json:"ended,omitempty"`
	Turn              int           `json:"turn,omitempty"`
	PostID            string        `json:"postId"`
	Posted            bool          `json:"posted,omitempty"`
	WrittenHandoff    string        `json:"writtenHandoff,omitempty"`
	Handoff           string        `json:"handoff,omitempty"`
	Dir               string        `json:"dir,omitempty"`
	Branch            string        `json:"branch,omitempty"`
	Source            string        `json:"source,omitempty"` // cooperative, transcript, native (same Pi session) or converted
	// ConvertedSession is the target session id of a cross-harness
	// conversion. It is saved before the file is written, so a restarted
	// coordinator writes the same session again instead of a second one.
	ConvertedSession string `json:"convertedSession,omitempty"`
	// CompactionExtended records that the startup deadline was extended once
	// because the resumed Pi session reported compaction progress. The next
	// deadline is final: a resume that still produces no output falls back.
	CompactionExtended bool   `json:"compactionExtended,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Error              string `json:"error,omitempty"`
	// Notice is the one session DM that explains an automatic fallback after
	// a usage limit. Limit says why the turn left the previous profile.
	Notice       string `json:"notice,omitempty"`
	Limit        string `json:"limit,omitempty"`
	NoticePosted bool   `json:"noticePosted,omitempty"`
}

var activeTransferPhases = []string{"waiting", "stopping", "starting"}

func (t *TransferState) Active() bool {
	return t != nil && slices.Contains(activeTransferPhases, t.Phase)
}
func (t *TransferState) blocksTurns() bool {
	return t != nil && (t.Phase == "stopping" || t.Phase == "starting")
}
func (m *Manager) transferNow() time.Time {
	if m.TransferNow != nil {
		return m.TransferNow()
	}
	return time.Now()
}
func (m *Manager) transferSleep(ctx context.Context, delay time.Duration) error {
	if m.TransferSleep != nil {
		return m.TransferSleep(ctx, delay)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

type TransferOptions struct {
	rootSeat bool

	// Fresh starts another native conversation after a completed transfer.
	// Internal lifecycle services use this for periodic context refreshes.
	Fresh bool `json:"-"`

	Profile     string `json:"profile"`
	Model       string `json:"model,omitempty"`
	WaitSeconds int    `json:"waitSeconds"` // 0 means immediate transcript fallback; maximum 120
	Retry       bool   `json:"retry,omitempty"`

	// Fallback marks an automatic move after a usage limit. Only the engine's
	// usage-limit fallback sets it: Notice is the one DM that explains the
	// move, and LimitReason says what the previous profile could not do.
	Fallback    bool   `json:"fallback,omitempty"`
	Notice      string `json:"notice,omitempty"`
	LimitReason string `json:"limitReason,omitempty"`
}

// TransferContext cancels preparation while waiting for its metadata lock.
// A transfer already saved remains durable if the caller later goes away.
func (m *Manager) TransferContext(ctx context.Context, ref string, opts TransferOptions) (TransferState, error) {
	if err := authority.Check("subagent transfer", authority.Of(ctx), nil); err != nil {
		return TransferState{}, err
	}
	return m.requestTransfer(ctx, ref, opts)
}

// requestTransfer is the shared transfer path for a caller that has already
// been checked: the CLI through TransferContext, and the engine's automatic
// usage-limit fallback, which is the engine's own work.
func (m *Manager) requestTransfer(ctx context.Context, ref string, opts TransferOptions) (TransferState, error) {
	if opts.WaitSeconds < 0 || opts.WaitSeconds > 120 {
		return TransferState{}, errors.New("handoff wait must be between 0 and 120 seconds")
	}
	ps, err := m.profiles()
	if err != nil {
		return TransferState{}, err
	}
	p, ok := ps.Get(opts.Profile)
	if !ok || p.Disabled {
		return TransferState{}, fmt.Errorf("unknown or disabled profile %q", opts.Profile)
	}
	candidate := *p
	if opts.Model != "" {
		candidate.Model = strings.TrimSpace(opts.Model)
		candidate.Models = nil
	}
	if len(candidate.Model) > 256 || strings.ContainsAny(candidate.Model, "\r\n\x00") {
		return TransferState{}, errors.New("invalid model name")
	}
	if err := candidate.Validate(); err != nil {
		return TransferState{}, err
	}
	info, err := m.store().Resolve(ref)
	if err != nil {
		return TransferState{}, err
	}
	var out TransferState
	err = m.store().withLockContext(ctx, info.ID, func() error {
		meta, err := m.store().ReadMeta(info.ID)
		if err != nil {
			return err
		}
		if meta.Closing.Active() {
			return errors.New("session has a dormant close reservation")
		}
		if !opts.rootSeat && m.RoomPaused(meta.RoomID) {
			return errRoomPaused
		}
		if err := checkProfileUse(*p, meta.Service && meta.Role != RoleRoot); err != nil {
			return err
		}
		if meta.Consolidating != "" {
			return errors.New("session consolidation is in progress")
		}
		if !meta.Closed.IsZero() {
			return ErrClosed
		}
		// The model this session runs now: its own choice, or the profile's
		// default for records written before per-turn profile snapshots.
		currentModel := meta.Model
		if currentModel == "" {
			if prior, ok := ps.Get(meta.Profile); ok {
				currentModel = prior.Model
			}
		}
		// A request that names the profile and model the session already runs
		// cannot move it. Start a fresh conversation instead, so repeating the
		// command after a completed transfer does something. A failed transfer
		// stays a record that only --retry repeats.
		fresh := opts.Fresh || !opts.rootSeat && !opts.Retry && meta.Profile == opts.Profile &&
			currentModel == candidate.Model && (meta.Transfer == nil || meta.Transfer.Phase == "completed")
		// A move between two Pi profiles keeps the session file: a resumed Pi
		// session takes the target model, its thinking level and its account,
		// and the whole conversation stays. A move from Pi to Claude Code
		// converts the session file instead. Both need no written handoff, so
		// the transfer stops the old turn and resumes at once. Every other pair
		// still starts a fresh native conversation.
		plan := transferPlanFor(ps, meta, candidate, fresh, opts)
		// A fallback chain continues from the profile the session is on. When the
		// turn that this transfer moved it to hits its own limit, that move is
		// over: the next move supersedes it here, in the same transaction. Its
		// coordinator then no longer owns the session, so it can neither reject
		// the next target as active nor restore the previous profile.
		supersede := opts.Fallback && meta.Transfer != nil && meta.Transfer.Phase == "starting" && meta.Transfer.Profile == meta.Profile
		if old := meta.Transfer; old != nil {
			same := old.Profile == opts.Profile && old.Model == candidate.Model && old.RootSeat == opts.rootSeat
			if old.Active() && !same && !supersede {
				return errors.New("a transfer is already active; cancel it before choosing another model")
			}
			if !supersede && (old.Active() || (same && !opts.Retry && !fresh)) {
				out = *old
				return nil
			}
			if opts.Retry && old.Phase != "failed" && old.Phase != "cancelled" {
				return errors.New("only failed or cancelled transfers can be retried")
			}
		}
		meta.Model = currentModel
		now := m.transferNow()
		token := NewID()
		phase, source, reason := "waiting", "", ""
		convertedSession := ""
		switch {
		case plan.native:
			phase, source, reason = "stopping", sourceNative, "the same Pi session keeps the conversation"
		case plan.convert:
			phase, source, reason = "stopping", sourceConverted, "converting the Pi session to Claude Code"
			// A retry rewrites the same target session, so one failed attempt
			// does not leave an orphaned Claude session file behind.
			convertedSession = sessionconv.NewSessionID()
			if opts.Retry && meta.Transfer != nil && meta.Transfer.ConvertedSession != "" {
				convertedSession = meta.Transfer.ConvertedSession
			}
		}
		out = TransferState{RootSeat: opts.rootSeat, ID: token, Session: meta.ID, Phase: phase, Profile: opts.Profile, Model: candidate.Model,
			PreviousProfile: meta.Profile, PreviousModel: meta.Model, PreviousHarness: meta.HarnessSession, PreviousDir: meta.Dir,
			PreviousResumable: meta.Resumable, OldTurns: meta.Turns, Requested: now, WaitFor: time.Duration(opts.WaitSeconds) * time.Second,
			Deadline: now.Add(time.Duration(opts.WaitSeconds) * time.Second), Source: source, Reason: reason,
			ConvertedSession: convertedSession, PostID: transferPostID(token)}
		if opts.Fallback {
			// Record the profiles this session has left and entered, so a later
			// limit continues the chain and never returns to one that just ran
			// out, not even a cyclic chain.
			out.Notice, out.Limit = opts.Notice, opts.LimitReason
			for _, name := range []string{meta.Profile, opts.Profile} {
				if !slices.Contains(meta.FallbackPath, name) {
					meta.FallbackPath = append(meta.FallbackPath, name)
				}
			}
		} else {
			// A deliberate move, by root or by a consolidation, starts the
			// optional chain over.
			meta.FallbackPath = nil
		}
		if previous := meta.Transfer; !plan.native && !plan.convert && !opts.rootSeat && previous != nil && previous.Phase == "failed" && previous.Source == "cooperative" {
			out.WrittenHandoff, out.Handoff, out.Dir, out.Branch, out.Source = previous.WrittenHandoff, previous.WrittenHandoff, previous.Dir, previous.Branch, "cooperative"
		}
		// Freeze labels for records written before per-turn profile snapshots.
		previousModel := currentModel
		for n := 1; n <= meta.Turns; n++ {
			turn, err := m.store().readTurnForUpdate(meta.ID, n)
			if err != nil {
				return err
			}
			if turn.Profile == "" {
				turn.Profile, turn.Model = meta.Profile, previousModel
				if err := m.store().writeTurn(meta.ID, turn); err != nil {
					return err
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		meta.Transfer = &out
		return m.store().writeMeta(meta)
	})
	if err != nil {
		return out, err
	}
	if out.Active() && !out.RootSeat {
		launch := m.TransferLaunch
		if launch == nil {
			launch = goTransfer
		}
		if err := launch(m, info.ID, out.ID); err != nil {
			_ = m.updateTransfer(info.ID, out.ID, func(_ *Meta, t *TransferState) error {
				t.Error = "coordinator could not start: " + err.Error()
				return nil
			})
			// Keep the pending record. Repeating the request can restart its worker.
			return out, fmt.Errorf("transfer is saved but its worker could not start: %w; repeat the transfer command to recover", err)
		}
	}
	return out, nil
}

// sourceNative marks a transfer that keeps the session's own native
// conversation instead of building a handoff packet.
const sourceNative = "native"

// sourceTranscript marks a transfer that continues in a fresh conversation
// built from a written handoff.
const sourceTranscript = "transcript"

// transferPlan names how a transfer continues the conversation: in the same
// Pi session, in a converted Claude Code session, or in a fresh conversation
// with a written handoff.
type transferPlan struct {
	native  bool
	convert bool
}

// transferPlanFor chooses the smallest continuation that keeps the history.
// A Pi session file can be resumed by another Pi profile as it is. It can also
// be converted into a Claude Code session, which then resumes with Claude's
// --resume. A fresh request, a root seat, a session without a resumable id and
// a missing prior profile keep the written-handoff path.
func transferPlanFor(ps *profile.Store, meta Meta, target profile.Profile, fresh bool, opts TransferOptions) transferPlan {
	if opts.rootSeat || fresh || !meta.Resumable || meta.HarnessSession == "" {
		return transferPlan{}
	}
	current, ok := ps.Get(meta.Profile)
	if !ok || current.Harness != profile.HarnessPi {
		return transferPlan{}
	}
	switch target.Harness {
	case profile.HarnessPi:
		return transferPlan{native: true}
	case profile.HarnessClaudeCode:
		return transferPlan{convert: true}
	}
	return transferPlan{}
}

func transferPostID(token string) string { // deterministic, no second room post on recovery
	return hashPostID(token)
}

// transferNoticeID is the fallback notice's deterministic ID, so a retried
// coordinator never posts it twice.
func transferNoticeID(token string) string { return hashPostID(token + ":notice") }

func hashPostID(seed string) string {
	var value uint64 = 14695981039346656037
	for _, b := range []byte(seed) {
		value ^= uint64(b)
		value *= 1099511628211
	}
	return fmt.Sprintf("msg_%016x", value)
}

func (m *Manager) TransferStatus(ref string) (*TransferState, error) {
	info, err := m.store().Resolve(ref)
	if err != nil {
		return nil, err
	}
	meta, err := m.store().ReadMeta(info.ID)
	return meta.Transfer, err
}

func (m *Manager) updateTransfer(id, token string, fn func(*Meta, *TransferState) error) error {
	return m.store().withLock(id, func() error {
		meta, err := m.store().ReadMeta(id)
		if err != nil {
			return err
		}
		if meta.Transfer == nil || meta.Transfer.ID != token {
			return errors.New("transfer no longer current")
		}
		if err := fn(&meta, meta.Transfer); err != nil {
			return err
		}
		return m.store().writeMeta(meta)
	})
}

// SubmitHandoff records a written handoff, plus the task's actual worktree and
// branch. A stale response can never satisfy a newer transfer.
func (m *Manager) SubmitHandoff(ctx context.Context, ref, token, text, dir, branch string) error {
	if err := authority.Check("subagent transfer handoff", authority.Of(ctx), nil); err != nil {
		return err
	}
	info, err := m.store().Resolve(ref)
	if err != nil {
		return err
	}
	return m.submitHandoff(info.ID, token, text, dir, branch)
}

// submitHandoff stores session id's handoff for transfer token. It checks
// no caller: a transfer's coordinator calls it for the old agent's
// [DYNA HANDOFF] answer, as the engine's own work.
func (m *Manager) submitHandoff(id, token, text, dir, branch string) error {
	var err error
	if strings.TrimSpace(text) == "" || len(text) > 32*1024 {
		return errors.New("handoff must contain 1 byte to 32 KiB")
	}
	if dir != "" {
		dir, err = filepath.Abs(dir)
		if err != nil {
			return err
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return errors.New("handoff directory does not exist")
		}
	}
	if len(branch) > 256 || strings.ContainsAny(branch, "\r\n\x00") {
		return errors.New("invalid branch hint")
	}
	return m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
		if t.Phase != "waiting" {
			return errors.New("transfer no longer accepts a handoff")
		}
		t.Handoff, t.WrittenHandoff, t.Dir, t.Branch, t.Source = text, text, dir, branch, "cooperative"
		return nil
	})
}

func (m *Manager) CancelTransfer(ctx context.Context, ref string) (TransferState, error) {
	if err := authority.Check("subagent transfer cancel", authority.Of(ctx), nil); err != nil {
		return TransferState{}, err
	}
	t, err := m.TransferStatus(ref)
	if err != nil {
		return TransferState{}, err
	}
	if t == nil {
		return TransferState{}, errors.New("no transfer")
	}
	meta, e := m.store().ReadMeta(t.Session)
	if e != nil {
		return TransferState{}, e
	}
	if meta.Consolidating != "" {
		return TransferState{}, errors.New("use consolidation cancel for this operation")
	}
	err = m.updateTransfer(t.Session, t.ID, func(_ *Meta, state *TransferState) error {
		if state.Phase == "cancelled" {
			return nil
		}
		if state.Phase != "waiting" {
			return errors.New("only a transfer waiting for handoff can be cancelled; model startup has already begun")
		}
		state.Phase, state.Ended = "cancelled", m.transferNow()
		return nil
	})
	if err == nil {
		_ = m.store().DeletePendingSteer(m.rooms(), t.Session, t.PostID)
	}
	current, _ := m.TransferStatus(t.Session)
	if current != nil {
		return *current, err
	}
	return *t, err
}

// goTransfer runs one transfer's coordinator on the Manager's Workers. The
// token names the worker, so a superseding fallback gets its own coordinator
// while the previous one is still registered and finishing; the session's
// transfer lock and the stored token keep them in order. Production callers
// run in the engine; a test Manager uses processWorkers, whose goroutines end
// with that process.
func goTransfer(m *Manager, id, token string) error {
	m.workers().Go("transfer "+id+" "+token, func(ctx context.Context) error { return m.runTransfer(ctx, id, token) })
	return nil
}

// runTransfer resumes durable work under a per-session worker lock. It never
// changes process-wide environment or starts two replacement turns. It is
// the engine's own work, so it checks no caller.
func (m *Manager) runTransfer(ctx context.Context, id, token string) error {
	meta, err := m.store().ReadMeta(id)
	if err != nil {
		return err
	}
	if meta.Consolidating != "" {
		return errors.New("resume the consolidation coordinator instead")
	}
	dir, err := m.store().Dir(id)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "transfer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	// A detached worker of the earlier release may still hold the lock; this
	// one goes on once it exits.
	for delay := 200 * time.Millisecond; ; delay = min(delay*2, time.Second) {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if err := m.transferSleep(ctx, delay); err != nil {
			return err
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
		if t.Active() {
			t.Error = ""
		}
		return nil
	})
	// Back off only while the durable state is unchanged. Phase transitions,
	// handoff submission and turn creation advance immediately.
	delay := 200 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if meta.Transfer == nil || !meta.Transfer.RootSeat {
			if err := m.waitRoom(ctx, meta.RoomID); err != nil {
				return err
			}
		}
		before, err := m.store().ReadMeta(id)
		if err != nil {
			return err
		}
		done, err := m.advanceTransfer(ctx, id, token)
		if errors.Is(err, errRoomPaused) {
			continue
		}
		if err != nil || done {
			if err != nil {
				_ = m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
					if t.Active() {
						t.Error = "coordinator paused: " + err.Error()
					}
					return nil
				})
			}
			return err
		}
		after, err := m.store().ReadMeta(id)
		if err != nil {
			return err
		}
		current := after.Transfer
		if current == nil || current.ID != token || !current.Active() {
			return nil
		}
		if before.Transfer == nil || *before.Transfer != *current || before.Turns != after.Turns {
			delay = 200 * time.Millisecond
			continue
		}
		pause := delay
		deadline := current.Deadline
		if current.Phase == "starting" {
			deadline = current.StartupDeadline
		}
		// The final poll lands on the deadline, even between backoff steps.
		remaining := deadline.Sub(m.transferNow())
		if current.Phase == "waiting" && remaining <= 0 {
			continue // The deadline passed during this poll's store operations.
		}
		if remaining > 0 && remaining < pause {
			pause = remaining
		}
		if err := m.transferSleep(ctx, pause); err != nil {
			return err
		}
		delay = min(delay*2, time.Second)
	}
}

func (m *Manager) advanceTransfer(ctx context.Context, id, token string) (bool, error) {
	meta, err := m.store().ReadMeta(id)
	if err != nil {
		return true, err
	}
	t := meta.Transfer
	if t == nil || t.ID != token || !t.Active() {
		return true, nil
	}
	// A root seat handoff has a wall-clock budget even while the room is paused.
	var budget time.Duration
	if !t.RootSeat {
		budget, err = m.coordinatorPauseBudget(ctx, meta.RoomID, t.Requested)
		if err != nil {
			return false, err
		}
	}
	if budget > t.PausedFor {
		err = m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
			delta := budget - t.PausedFor
			if delta > 0 {
				t.Deadline = t.Deadline.Add(delta)
				if !t.StartupDeadline.IsZero() {
					t.StartupDeadline = t.StartupDeadline.Add(delta)
				}
				t.PausedFor = budget
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		meta, err = m.store().ReadMeta(id)
		if err != nil {
			return false, err
		}
		t = meta.Transfer
	}
	if !meta.Closed.IsZero() {
		return true, m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
			t.Phase, t.Reason, t.Ended = "cancelled", "session closed", m.transferNow()
			return nil
		})
	}
	now := m.transferNow()
	last, _ := m.store().ReadTurn(id, meta.Turns)
	switch t.Phase {
	case "waiting":
		if t.Posted && last.Done() && last.Ended.After(t.Requested) {
			if result, ok := last.Result.(string); ok && strings.HasPrefix(strings.TrimSpace(result), "[DYNA HANDOFF]") && t.Handoff == "" {
				err := m.submitHandoff(id, token, result, "", "")
				return false, err
			}
		}
		// A running turn cannot answer the handoff ask, so hold the ask until it
		// ends. The hold is bounded by the request deadline: a turn that runs past
		// the whole window is stopped, and the transcript fallback takes over, so
		// a transfer never waits without end. When the ask goes out the window
		// restarts, so the old model still gets its full handoff length. A
		// zero-window transfer (WaitFor 0) falls back at once, without asking.
		holdForTurn := !t.Posted && !last.Done() && t.WaitFor > 0
		timedOut := !now.Before(t.Deadline)
		reason := ""
		switch {
		case t.Handoff != "":
			reason = "written handoff received"
		case last.Status == TurnError:
			reason = "previous agent failed: " + last.Error
		case timedOut:
			reason = "handoff wait ended"
		case !meta.Resumable || meta.HarnessSession == "":
			reason = "previous agent has no resumable conversation"
		}
		if reason != "" {
			return false, m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
				if t.Phase == "waiting" {
					t.Phase, t.Reason = "stopping", reason
				}
				return nil
			})
		}
		if holdForTurn {
			// Keep the poller sleeping until the turn ends or the deadline above
			// ends the hold. The ask, and its own window, start only after that.
			return false, nil
		}
		// The ask is a typed session event, so its card says that a transfer
		// needs a handoff and the agent text keeps the full instructions.
		instructions := fmt.Sprintf("Prepare a handoff for the replacement model. Include the goal, decisions, completed work, next actions, blockers, actual task worktree, branch, dirty files, tests, PRs and recovery commands. Submit it with `dyna subagent transfer handoff %s --token %s --dir <actual-task-directory> --branch <branch> <text-or-@file>`. Or begin your final answer with [DYNA HANDOFF]. Preserve all files. A bounded wait is active. A cancelled transfer can reject a late handoff.", id, token)
		if !t.Posted {
			err = m.updateTransfer(id, token, func(meta *Meta, t *TransferState) error {
				if t.Phase != "waiting" {
					return nil
				}
				event := room.SessionEvent{Type: room.SessionTransfer, Session: meta.Address(), Profile: t.Profile, Transfer: token, Detail: instructions}
				if _, err := m.rooms().PostEventInput(room.PostEventInput{RoomID: meta.RoomID, ID: t.PostID, Event: event, To: []string{meta.Address()}, Via: []string{"@" + meta.Address()}, SessionIDs: map[string]string{meta.Address(): id}}); err != nil {
					return err
				}
				t.Posted = true
				// The handoff window starts when the ask goes out, so the old model
				// gets its full length after a turn that ended within the hold. A
				// shared or zero-window deadline (consolidation) is left untouched.
				if t.WaitFor > 0 {
					t.Deadline = now.Add(t.WaitFor)
				}
				return nil
			})
			if err != nil {
				return true, err
			}
		}
		current, err := m.TransferStatus(id)
		if err != nil {
			return true, err
		}
		if current == nil || current.ID != token || current.Phase != "waiting" {
			return true, nil
		}

		return false, nil
	case "stopping":
		// A cancelled request must not become input to the replacement if it was
		// never claimed by the old model (for example during prompt preparation).
		_ = m.store().DeletePendingSteer(m.rooms(), id, t.PostID)
		if m.store().lockHeld(id) {
			stopCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			if _, err := m.stopSession(stopCtx, id, meta.Consolidating, "transfer"); err != nil {
				return true, m.failTransfer(id, token, err)
			}
		}
		// Resolve the target profile first: a conversion needs its model and
		// harness, and every path below needs it.
		ps, err := m.profiles()
		if err != nil {
			return true, m.failTransfer(id, token, err)
		}
		p, ok := ps.Get(t.Profile)
		if !ok || p.Disabled {
			return true, m.failTransfer(id, token, errors.New("target profile is unavailable"))
		}
		chosen := *p
		chosen.Model = t.Model
		chosen.Models = nil
		if err := chosen.Validate(); err != nil {
			return true, m.failTransfer(id, token, err)
		}
		// A converted session is written before the old conversation is
		// replaced. Any failure keeps the turn, the session id and the
		// worktree as they are and falls back to the written handoff below.
		native := t.Source == sourceNative
		convert := t.Source == sourceConverted
		attemptedConvert := convert
		convertedID := ""
		conversionReason := ""
		if convert {
			convertedID, err = m.convertPiToClaude(ctx, meta, *t, chosen)
			if err != nil {
				convert = false
				conversionReason = "session conversion unavailable: " + clipped(err.Error(), 300) + "; using a written handoff"
			}
		}
		var packet, dir, prompt string
		switch {
		case native:
			// The session file already holds the conversation, the files and the
			// tool state, so no packet is built and the worktree stays as it is.
			dir = meta.Dir
			prompt = fmt.Sprintf("[DYNA MODEL TRANSFER] Continue this session. It now runs on %s / %s in the same Pi conversation, so the full history is intact. Resume the open work.\n", t.Profile, orNone(t.Model))
		case convert:
			// The converted Claude session keeps the same conversation and the
			// same worktree; the target turn resumes it by its new id.
			dir = meta.Dir
			prompt = fmt.Sprintf("[DYNA MODEL TRANSFER] Continue this session. Its conversation was converted from Pi to Claude Code as session %s, so the full history is intact. Resume the open work.\n", convertedID)
		default:
			packet, dir, err = m.handoffPacket(meta, *t)
			if err != nil {
				return true, m.failTransfer(id, token, err)
			}
			if t.RootSeat {
				return true, m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
					t.Phase, t.Ended = "completed", now
					t.Handoff = packet
					if t.Source == "" {
						t.Source = sourceTranscript
					}
					return nil
				})
			}
			prompt = "[DYNA MODEL TRANSFER] Continue this stable session in a fresh native conversation.\n" + packet
		}
		previousHarness := ""
		if previous, ok := ps.Get(t.PreviousProfile); ok {
			previousHarness = previous.Harness
		}
		err = m.updateTransfer(id, token, func(meta *Meta, t *TransferState) error {
			if t.Phase != "stopping" {
				return errors.New("transfer changed")
			}
			if !meta.Closed.IsZero() {
				return ErrClosed
			}
			startup := now.Add(60 * time.Second)
			if profile.CompactDue(chosen, meta.ContextTokens) {
				// Dyna's own compact command runs before the first request on the
				// resumed session. Give it a second window so a large history is
				// not mistaken for a stalled model.
				startup = now.Add(2 * 60 * time.Second)
			}
			t.Phase, t.Turn, t.StartupDeadline = "starting", meta.Turns+1, startup
			t.OldTurns = meta.Turns
			meta.Profile, meta.Model, meta.Dir = t.Profile, t.Model, dir
			switch {
			case native:
				// A native Pi move keeps HarnessSession and Resumable: the new model
				// resumes the same session, and the turn's own account selection
				// gives the target provider's active account home.
				t.Source = sourceNative
			case convert:
				t.ConvertedSession = convertedID
				t.Source = sourceConverted
				t.Reason = "converted the Pi session to Claude Code session " + convertedID
				meta.HarnessSession, meta.Resumable = convertedID, true
			default:
				if attemptedConvert {
					t.Source, t.Reason = sourceTranscript, conversionReason
				} else if t.Source == "" {
					t.Source = sourceTranscript
				}
				meta.HarnessSession, meta.Resumable = "", harness.Resumable(chosen)
			}
			// A harness change must not keep the old harness's runtime pin; the
			// next turn refuses a pin whose native harness differs. Clearing it
			// here also covers a profile change that crosses harnesses (D-109).
			if chosen.Harness != previousHarness {
				meta.RuntimePin = nil
			}
			t.Handoff = prompt // durable, so a dead worker can restart before turn creation
			return nil
		})
		if err == nil {
			m.postFallbackNotice(id, token)
		}
		return false, err
	case "starting":
		if meta.Turns < t.Turn {
			_, _, err := m.beginTransferTurn(id, t.Handoff, token)
			if errors.Is(err, errRoomPaused) {
				return false, err
			}
			if err != nil {
				return true, m.failTransfer(id, token, err)
			}
			return false, nil
		}
		turn, err := m.store().ReadTurn(id, t.Turn)
		if err != nil {
			return true, m.failTransfer(id, token, err)
		}
		if turn.TransferID != token {
			return true, m.failTransfer(id, token, errors.New("replacement turn does not match the transfer"))
		}
		if turn.Status == TurnError || turn.Status == TurnStopped {
			if m.store().lockHeld(id) {
				return false, nil
			}
			if (t.Source == sourceNative || t.Source == sourceConverted) && turn.Status == TurnError && !m.store().transferHasOutput(id, turn) {
				// The resumed session may be missing or unusable on the target
				// model, and a converted file may not load. The fresh conversation
				// is the bounded fallback, and it runs once: its own failure fails
				// the move as before.
				converted := t.Source == sourceConverted
				err := m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
					t.Phase, t.Turn, t.StartupDeadline = "stopping", 0, time.Time{}
					t.Source = sourceTranscript
					if converted {
						t.ConvertedSession = ""
						t.Reason = "the converted Claude session did not load; using a written handoff: " + clipped(turn.Error, 300)
					} else {
						t.Reason = "Pi could not resume the session; using a fresh conversation: " + clipped(turn.Error, 300)
					}
					return nil
				})
				return false, err
			}
			return true, m.failTransfer(id, token, fmt.Errorf("replacement startup failed: %s", turn.Error))
		}
		if turn.Status == TurnOK || m.store().transferHasOutput(id, turn) {
			err := m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error { t.Phase, t.Ended = "completed", now; return nil })
			return true, err
		}
		if (t.Source == sourceNative || t.Source == sourceConverted) && !t.CompactionExtended && m.transferCompactionSeen(id, turn) {
			// Pi compacts a large kept history before its first request. The
			// compaction is startup progress, not a stalled model, so add one
			// bounded window. Extend the existing deadline instead of replacing
			// it: the event arrives seconds after launch, and a due pre-turn
			// compact already has a longer window that must not shrink.
			err := m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
				t.CompactionExtended = true
				t.StartupDeadline = t.StartupDeadline.Add(60 * time.Second)
				return nil
			})
			return false, err
		}
		if !now.Before(t.StartupDeadline) {
			if m.store().lockHeld(id) {
				stopCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()
				if _, err := m.stopSession(stopCtx, id, meta.Consolidating, transferStartupTimeout); err != nil {
					return true, err
				}
			}
			if t.Source == sourceNative || t.Source == sourceConverted {
				// The resumed conversation still produced nothing. Use the
				// bounded written handoff instead of failing the whole move.
				return false, m.updateTransfer(id, token, func(_ *Meta, t *TransferState) error {
					t.Phase, t.Turn, t.StartupDeadline = "stopping", 0, time.Time{}
					t.Source = sourceTranscript
					t.Reason = "the resumed session produced no model output; using a written handoff"
					return nil
				})
			}
			return true, m.failTransfer(id, token, errors.New(transferStartupTimeout))
		}
	}
	return false, nil
}

func (m *Manager) failTransfer(id, token string, cause error) error {
	err := m.updateTransfer(id, token, func(meta *Meta, t *TransferState) error {
		if t.Phase == "starting" {
			meta.Profile, meta.Model, meta.HarnessSession, meta.Resumable, meta.Dir = t.PreviousProfile, t.PreviousModel, t.PreviousHarness, t.PreviousResumable, t.PreviousDir
		}
		t.Phase, t.Error, t.Ended = "failed", cause.Error(), m.transferNow()
		return nil
	})
	if err != nil {
		return err
	}
	return cause
}

// beginTransferTurn owns an explicit token rather than faking a caller's parent.
func (m *Manager) beginTransferTurn(id, prompt, token string) (Turn, bool, error) {
	meta, err := m.store().ReadMeta(id)
	if err != nil {
		return Turn{}, false, err
	}
	return m.beginTurnConfigured(id, prompt, "", []string{m.store().ReportTarget(meta)}, "", token, false, time.Time{})
}
