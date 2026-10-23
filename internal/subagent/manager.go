package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/lockrank"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/pr/links"
	"dyna-agent/internal/pr/review"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/settings"
	storepkg "dyna-agent/internal/store"
	"dyna-agent/internal/structuredoutput"
	"dyna-agent/internal/workeroutput"
)

// Spawner launches the runner of one turn. The runner inherits the held turn
// lock and releases it by exiting (or closing it) when the turn is over.
type Spawner interface {
	Spawn(m *Manager, id string, turn int, lock *os.File) error
}

// Manager is the subagent API used by the CLI and the desktop app.
type Manager struct {
	// TurnReporter schedules the normal report, or uses the supplied terminal
	// context for an earlier engine that cannot own completion yet.
	TurnReporter     func(context.Context, *Manager, string, int) error
	ResumeRoomLaunch func(*Manager, string, uint64) error
	Store            *Store
	Rooms            *room.Store
	// Profiles loads the profile registry (default: the user's profiles.json).
	Profiles func() (*profile.Store, error)
	// Settings loads user settings (default: settings.json).
	Settings func() settings.Values
	Spawner  Spawner
	// RecoveryDelay overrides the harness recovery wait for tests and embedding.
	// Nil keeps the normal delay.
	RecoveryDelay *time.Duration
	// ProcessRunner replaces harness process launch for tests and embedding.
	ProcessRunner harness.ProcessRunner
	// Executable is the dyna binary workers and runners use (default: this one).
	Executable string
	// Engine is set in a detached turn runner: its session writes go to the
	// engine through an ordered outbox (item 2). Nil writes the store
	// directly, as a runner inside the engine does.
	Engine *engineclient.Client
	// InputEvents is the engine's existing change hub. Detached runners wait
	// through the read-only engine call, while their outbox sends mutations.
	InputEvents       *hub.Hub
	inputWaitDeadline func() (<-chan time.Time, func())
	// Workers runs session workers, such as transfer coordinators, on the
	// engine's run context. The engine sets it before it serves; nil runs
	// them on this process's own Workers.
	Workers *Workers
	// Transfer hooks keep tests and embedded callers isolated and deterministic.
	TransferLaunch      func(*Manager, string, string) error
	ConsolidationLaunch func(*Manager, string, string) error
	TransferNow         func() time.Time
	TransferSleep       func(context.Context, time.Duration) error

	// ClosePRs hands live PR roles to the heir through the PR service.
	ClosePRs func(context.Context, Info, Info) ([]string, error)

	WrapUpProviders []WrapUpProvider

	// InstructionsChanged is called after NoteRoomInstructions stores a
	// quiet period, so the engine can arm the note's timer. The engine sets
	// it on its Manager before it serves; a CLI Manager leaves it nil, and
	// the engine catches its writes by event.
	InstructionsChanged func()

	// checkpoint lets tests act inside a lifecycle race window. Stages:
	// "dormant-checked", "dismiss-reserved", "wake-selected", "turn-started",
	// "turn-finished", "close-before-stop", "close-before-goal-handoff", and "cleanup-start".
	checkpoint func(stage string)

	// RoomFileSize measures a room log for a terminal input cutoff. Tests use
	// it to fail the measurement; production leaves it nil.
	RoomFileSize func(roomID string) (int64, error)
}

// NewManager returns a manager over the default store and a detached-process
// spawner.
func NewManager() *Manager {
	return &Manager{Store: DefaultStore(), Spawner: ExecSpawner{}}
}

func (m *Manager) store() *Store {
	if m.Store == nil {
		m.Store = DefaultStore()
	}
	return m.Store
}

// RoomStore returns the room store paired with this manager's session store.
func (m *Manager) RoomStore() *room.Store { return m.rooms() }

// FileSize measures a room log for a terminal input cutoff.
func (m *Manager) FileSize(roomID string) (int64, error) {
	if m.RoomFileSize != nil {
		return m.RoomFileSize(roomID)
	}
	return m.rooms().FileSize(roomID)
}

func (m *Manager) rooms() *room.Store {
	if m.Rooms != nil {
		return m.Rooms
	}
	m.Rooms = &room.Store{Root: filepath.Join(filepath.Dir(m.store().Root), "rooms")}
	return m.Rooms
}

func (m *Manager) resolveStartRoom(requested string) (room.Record, error) {
	return m.rooms().ResolveContext(requested, true)
}

// startDir is the child's working directory. Without --dir, a session's child
// starts in the parent session's directory, not in whatever directory the
// parent's shell happens to be in; other callers use their Cwd. An implicit
// directory outside a git work tree fails when the room has a repository, so
// a child never lands in the wrong place silently.
func (m *Manager) startDir(opts StartOptions, rec room.Record, parent string) (string, error) {
	dir := opts.Dir
	if dir == "" && parent != "" {
		if meta, err := m.store().ReadMeta(parent); err == nil {
			dir = meta.Dir
		}
	}
	if dir == "" {
		dir = opts.Cwd
	}
	if dir == "" {
		return "", errors.New("starting a session requires a working directory; supply Dir or Cwd")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", dir)
	}
	if opts.Dir == "" && rec.Repo != "" {
		if _, err := room.RepoRoot(dir); err != nil {
			return "", fmt.Errorf("working directory %q is not in a git work tree, but room %s has repository %s; pass --dir <path> explicitly", dir, rec.Name, rec.Repo)
		}
	}
	return dir, nil
}

// ProfileStore loads the profile registry this manager starts sessions with.
func (m *Manager) ProfileStore() (*profile.Store, error) { return m.profiles() }

func (m *Manager) profiles() (*profile.Store, error) {
	if m.Profiles != nil {
		return m.Profiles()
	}
	return profile.Load(profile.DefaultPath())
}

func (m *Manager) settings() settings.Values {
	if m.Settings != nil {
		return m.Settings()
	}
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		return settings.Defaults()
	}
	return v
}

// executable names the dyna binary a worker and its harness MCP servers run.
// It must outlive this process: a pinned /proc/<pid>/exe path fails once its
// process exits, which left later turns without the Dyna tools.
func (m *Manager) executable() string {
	if m.Executable != "" {
		return m.Executable
	}
	if exe, err := fsutil.StableExecutable(); err == nil {
		return exe
	}
	exe, _ := fsutil.RunningExecutable()
	return exe
}

// StartOptions describes a new session and its first turn.
type StartOptions struct {
	Service   bool      // exclude this managed session from general group posts
	Deadline  time.Time // absolute turn deadline; zero keeps ordinary sessions unlimited
	Role      string    // stored service role; empty for user agents
	Profile   string    // empty = the default profile
	Name      string    // optional unique label among open sessions in the room
	Room      string    // room name or ID; empty inherits or uses global
	Dir       string    // worker working directory (default: the parent's, else Cwd)
	Cwd       string    // the caller's working directory, the default Dir for a top-level session
	Owner     string    // an owner caller's own label, such as codex:<thread>; a session's is its session
	Schema    string    // optional JSON Schema for the first turn's result
	Goal      string    // required outcome and done condition; the session's goal, or its item's text when delegating
	Under     string    // explicit parent goal ID to delegate from; empty picks deterministically
	Task      string    // brief: context, owned files, checks and limits
	MessageID string    // stored input for the first turn, instead of a task brief
}

// ErrNameTaken matches the error Start returns when an open session in the
// room already holds the requested name.
var ErrNameTaken = errors.New("session name taken")

type nameTakenError struct{ room, name string }

func (e nameTakenError) Error() string {
	return fmt.Sprintf("an open subagent session in room %q is already named %q; pick another name", e.room, e.name)
}

func (e nameTakenError) Is(target error) bool { return target == ErrNameTaken }

// Start creates a session for the caller on ctx and launches its first turn
// detached. A caller with a session is the parent and may start only in that
// session's room. A caller without a session starts at the top level. Only
// the owner or root may ask for a service role, which never has a parent.
func (m *Manager) Start(ctx context.Context, opts StartOptions) (Info, Turn, error) {
	actor := authority.Of(ctx)
	if err := authority.Check("subagent start", actor, nil); err != nil {
		return Info{}, Turn{}, err
	}
	if isServiceRole(opts.Role) && actor.Role != authority.Owner && actor.Role != authority.Root {
		return Info{}, Turn{}, errors.New("only the owner or root may start a service role")
	}
	parent, owner := "", actor.Session
	switch {
	case actor.Session != "" && !isServiceRole(opts.Role):
		parent = actor.Session
	case actor.Role == authority.Owner:
		owner = opts.Owner
	}
	return m.start(opts, parent, owner)
}

// StartForEngine starts a session for the engine's own work, for no caller:
// a room's root, librarian or reviewer, or the agent that a
// PR notice escalates to. It checks no caller and sets no parent. It must never be an engine call
// (TestEngineMaintenanceIsNeverACall).
func (m *Manager) StartForEngine(opts StartOptions) (Info, Turn, error) {
	return m.start(opts, "", "")
}

// start creates the session under parent, which is "" for a top-level one,
// with owner as its owner label.
func (m *Manager) start(opts StartOptions, parent, owner string) (Info, Turn, error) {
	goal := strings.TrimSpace(opts.Goal)
	if goal == "" {
		return Info{}, Turn{}, errors.New(`--goal is required: pass the outcome and done condition, as in dyna subagent start --goal "<outcome; done when>" "<brief>"`)
	}
	if err := goals.CheckText(goal); err != nil {
		return Info{}, Turn{}, fmt.Errorf("--goal: %w", err)
	}
	task := strings.TrimSpace(opts.Task)
	if task == "" && opts.MessageID == "" {
		return Info{}, Turn{}, errors.New("a brief is required: pass the context, owned files, checks and limits after --goal")
	}
	if opts.Name != "" && (opts.Role != RoleRoot || opts.Name != RoleRoot) {
		if err := ValidateName(opts.Name); err != nil {
			return Info{}, Turn{}, err
		}
		if err := checkServiceName(opts.Name, opts.Role); err != nil {
			return Info{}, Turn{}, err
		}
	}
	if opts.Schema != "" {
		if err := structuredoutput.CheckSchema(opts.Schema); err != nil {
			return Info{}, Turn{}, err
		}
	}
	profs, err := m.profiles()
	if err != nil {
		return Info{}, Turn{}, err
	}
	// Dyna's services pick from service profiles. The internal root is the
	// owner's orchestrator and picks like a subagent session.
	service := opts.Service || isServiceRole(opts.Role) && opts.Role != RoleRoot
	var prof *profile.Profile
	if opts.Profile != "" {
		p, ok := profs.Get(opts.Profile)
		if !ok {
			return Info{}, Turn{}, fmt.Errorf("unknown profile %q; run `dyna profiles list` to see registered profiles", opts.Profile)
		}
		if p.Disabled {
			return Info{}, Turn{}, fmt.Errorf("profile %q is disabled; pick another from `dyna profiles list`", opts.Profile)
		}
		prof = p
	}
	roomRec, err := m.resolveStartRoom(opts.Room)
	if err != nil {
		return Info{}, Turn{}, err
	}
	dir, err := m.startDir(opts, roomRec, parent)
	if err != nil {
		return Info{}, Turn{}, err
	}

	store := m.store()
	if err := os.MkdirAll(store.root(), 0o755); err != nil {
		return Info{}, Turn{}, err
	}
	unlock, err := lockNames(store)
	if err != nil {
		return Info{}, Turn{}, err
	}
	defer unlock()
	// A running process keeps its original DYNA_DEPTH. Stored metadata is
	// authoritative after promotion, including for descendants already running.
	depth := 1
	parentService, parentRoot := false, false
	if parent != "" {
		meta, err := store.ReadMeta(parent)
		if err != nil {
			return Info{}, Turn{}, fmt.Errorf("cannot read parent session: %w", err)
		}
		if meta.Closing.Active() {
			return Info{}, Turn{}, errors.New("parent is being checked for dormant close")
		}
		if !meta.Closed.IsZero() {
			return Info{}, Turn{}, ErrClosed
		}
		if meta.RoomID != roomRec.ID {
			return Info{}, Turn{}, errors.New("parent session is in another room")
		}
		depth = meta.Depth + 1
		parentService, parentRoot = meta.Service, meta.Role == RoleRoot
	}
	// A service's helpers are services too; the internal root's are not.
	service = service || parentService && !parentRoot
	if prof == nil {
		pick := profs.SubagentDefaultProfile
		if service {
			pick = profs.ServiceDefaultProfile
		}
		p, ok := pick()
		if !ok {
			return Info{}, Turn{}, errors.New("no usable profile; run `dyna profiles list` and `dyna profiles add` or `dyna profiles set --use`")
		}
		prof = p
	}
	if err := checkProfileUse(*prof, service); err != nil {
		return Info{}, Turn{}, err
	}
	if err := review.CheckReviewerHelper(reviewerHierarchy{m.store()}, parent, *prof, profs); err != nil {
		return Info{}, Turn{}, err
	}
	if max := m.settings().SubagentMaxDepth; max > 0 && depth > max {
		return Info{}, Turn{}, fmt.Errorf("subagent depth limit reached: this session would be at depth %d but subagent.maxDepth is %d (raise it with `dyna settings set subagent.maxDepth N`, or do the work in the current session)", depth, max)
	}
	id := NewID()
	name := opts.Name
	if name == "" {
		base := "agent-" + id[len(id)-8:]
		name = base
		for suffix := 2; ; suffix++ {
			taken, err := store.nameTaken(roomRec.ID, name)
			if err != nil {
				return Info{}, Turn{}, err
			}
			if !taken {
				break
			}
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
	} else {
		taken, err := store.nameTaken(roomRec.ID, name)
		if err != nil {
			return Info{}, Turn{}, err
		}
		if taken {
			return Info{}, Turn{}, nameTakenError{room: roomRec.Name, name: name}
		}
	}
	sdir, _ := store.Dir(id)
	for _, sub := range []string{"turns", filepath.Join("agents", "1")} {
		if err := os.MkdirAll(filepath.Join(sdir, sub), 0o755); err != nil {
			return Info{}, Turn{}, err
		}
	}
	for _, name := range []string{"stdout.log", "output.jsonl"} {
		if err := os.WriteFile(filepath.Join(sdir, "agents", "1", name), nil, 0o644); err != nil {
			return Info{}, Turn{}, err
		}
	}
	snapshot := workeroutput.SelectedSnapshot{Version: workeroutput.SchemaVersion, Profile: workeroutput.ProfileSnapshot{
		Name: prof.Name, Model: prof.Model, Output: prof.EffectiveOutput(),
	}}
	if err := storepkg.WriteJSON(filepath.Join(sdir, "agents", "1", "output-config.json"), snapshot, 0o600); err != nil {
		return Info{}, Turn{}, err
	}
	now := time.Now()
	roomCursor, _ := m.rooms().FileSize(roomRec.ID)
	meta := Meta{
		ID: id, Name: name, Role: opts.Role, Service: service || opts.Role == RoleRoot, RoomID: roomRec.ID, Room: roomRec.Name, RoomCursor: roomCursor,
		Profile: prof.Name, Model: prof.Model, Dir: dir, Depth: depth, ParentSession: parent,
		Owner: owner, Created: now, LastActivity: now, Resumable: harness.Resumable(*prof),
	}
	if err := store.writeMeta(meta); err != nil {
		return Info{}, Turn{}, err
	}
	if meta.Goal, meta.Item, err = m.addSpawnGoal(meta, opts.Under, goal, task); err == nil {
		if err = store.writeMeta(meta); err != nil {
			if meta.Item == "" {
				_, _ = m.goalStore().Finish(roomRec.ID, meta.Goal, id, "The session failed to start.", true)
			} else {
				_, _ = m.goalStore().SetItemState(roomRec.ID, meta.Goal, meta.Item, id, goals.ItemDropped)
			}
		}
	}
	if err != nil {
		_ = sessionlife.Delete(store.root(), id)
		_ = os.RemoveAll(sdir)
		return Info{}, Turn{}, err
	}
	_ = m.rooms().Touch(roomRec.ID)
	store.appendEvent(id, map[string]any{"t": "session_start", "profile": prof.Name, "depth": depth, "parent": parent, "owner": meta.Owner, "name": name, "room": roomRec.Name, "address": meta.Address(), "dir": dir, "cwd": opts.Cwd})
	turn, _, err := m.beginTurnConfigured(id, task, opts.Schema, []string{store.ReportTarget(meta)}, opts.MessageID, "", false, opts.Deadline)
	if err != nil {
		return Info{}, Turn{}, err
	}
	info, _ := store.Info(id)
	return info, turn, nil
}

func lockNames(store *Store) (func(), error) {
	return lockNamesContext(context.Background(), store)
}

func lockNamesContext(ctx context.Context, store *Store) (func(), error) {
	release := lockrank.Acquire(lockrank.Names)
	unlock, err := storepkg.LockContext(ctx, filepath.Join(store.root(), ".names.lock"))
	if err != nil {
		release()
		return nil, err
	}
	return func() {
		unlock()
		release()
	}, nil
}

// beginTurnConfigured allocates and starts a turn through the shared path.
// Stored post IDs are checked again under the turn lock and cancellation gate.
func (m *Manager) beginTurnConfigured(id, prompt, schema string, owe []string, messageID, transferID string, wrapUp bool, deadline time.Time) (Turn, bool, error) {
	store := m.store()
	var turn Turn
	alreadyDelivered := false
	var lock *os.File
	err := store.withLock(id, func() error {
		meta, err := store.ReadMeta(id)
		if err != nil {
			return err
		}
		if !meta.Closed.IsZero() {
			return fmt.Errorf("%w: %s", ErrClosed, id)
		}
		if meta.Closing.Active() {
			return errors.New("session is being checked for dormant close")
		}
		if m.RoomPaused(meta.RoomID) && meta.Turns > 0 {
			return errRoomPaused
		}
		if meta.ConsumedBy != "" {
			return errors.New("session was consolidated; use the kept session")
		}
		if meta.Consolidating != "" && transferID == "" && (meta.Transfer == nil || messageID != meta.Transfer.PostID || meta.Transfer.Phase != "waiting") {
			return errors.New("session consolidation is in progress; posts remain stored")
		}
		if meta.Transfer != nil && messageID == meta.Transfer.PostID && meta.Transfer.Phase != "waiting" {
			return errors.New("handoff request is no longer active")
		}
		if meta.Transfer.blocksTurns() && transferID != meta.Transfer.ID {
			return errors.New("model transfer is in progress; posts remain stored for the replacement")
		}
		if transferID != "" && (meta.Transfer == nil || meta.Transfer.ID != transferID || meta.Transfer.Turn != meta.Turns+1) {
			return errors.New("stale transfer turn")
		}
		lock, err = store.tryTurnLock(id)
		if err != nil {
			return err
		}
		if messageID != "" {
			previous, delivered, err := store.deliveredPostTurn(id, messageID)
			if err != nil {
				return err
			}
			if delivered {
				turn, alreadyDelivered = previous, true
				return nil
			}
		}
		// Cancellation and turn allocation share the gate. A post cancelled
		// after the worker scan must not allocate an empty follow-up.
		return store.withGateLock(id, func(dir string) error {
			var queue queueClaims
			if messageID != "" {
				var err error
				queue, err = readQueueForUpdate(dir)
				if err != nil {
					return err
				}
				if queue.Cancelled[messageID] || queue.Dismissed[messageID] {
					turn, err = store.ReadTurn(id, meta.Turns)
					alreadyDelivered = true
					return err
				}
			}
			raw, _ := store.RawOutputPath(id)
			norm, _ := store.NormalizedPath(id)
			meta.Turns++
			meta.LastActivity = time.Now()
			meta.Status, meta.Finished = StatusIdle, time.Time{}
			roomPause, pauseErr := m.rooms().SessionPause(meta.RoomID)
			if pauseErr != nil {
				return pauseErr
			}
			turn = Turn{
				Deadline: deadline,
				RoomHeld: roomPause.Paused || roomPause.ResumePending,
				N:        meta.Turns, Prompt: prompt, Schema: schema, WrapUp: wrapUp, Status: TurnQueued, Created: time.Now(),
				Profile: meta.Profile, Model: meta.Model, TransferID: transferID,
				RawStart: fileSize(raw), NormStart: fileSize(norm),
			}
			for _, addr := range owe {
				turn.Owe(addr)
			}
			if schema != "" && len(turn.ReportTo) == 0 {
				turn.Owe(store.ReportTarget(meta))
			}
			if err := store.writeTurn(id, turn); err != nil {
				return err
			}
			if err := store.writeMeta(meta); err != nil {
				return err
			}
			if messageID != "" {
				queue.claim(messageID, turn.N)
				if err := writeQueue(dir, queue); err != nil {
					return err
				}
			}
			return writeGateState(dir, gateState{Active: true, Turn: turn.N})
		})
	})
	if err != nil {
		if lock != nil {
			lock.Close()
		}
		return Turn{}, false, err
	}
	if alreadyDelivered {
		lock.Close()
		return turn, true, nil
	}
	spawner := m.Spawner
	if spawner == nil {
		spawner = ExecSpawner{}
	}
	if err := spawner.Spawn(m, id, turn.N, lock); err != nil {
		meta, metaErr := store.ReadMeta(id)
		cutoffErr := metaErr
		var cutoff *int64
		if metaErr == nil {
			cutoff, cutoffErr = m.measureCutoff(context.Background(), meta.RoomID)
		}
		if cutoffErr == nil {
			_, _ = store.UpdateTurn(id, turn.N, func(t *Turn) {
				t.Status, t.Error, t.Ended = TurnError, "could not start the turn runner: "+err.Error(), time.Now()
				t.InputCutoff = cutoff
			})
		} else {
			err = errors.Join(err, cutoffErr)
		}
		lock.Close()
		return Turn{}, false, err
	}
	return turn, false, nil
}

// WakeResult says how a session will read a post.
type WakeResult struct {
	Session string `json:"session"`
	Turn    int    `json:"turn"`
	// Steered is true when an existing turn read or will read the post; false when
	// the post started a follow-up turn.
	Steered bool `json:"steered"`
	// Queued is true when that current turn is still preparing its prompt.
	Queued bool `json:"queued,omitempty"`
	Paused bool `json:"paused,omitempty"`
}

// WakeOptions tunes Wake. Schema applies only when the wake starts a
// follow-up turn.
type WakeOptions struct {
	// RequireIdle rejects steering and returns ErrBusy if another turn owns the lock.
	RequireIdle bool
	Deadline    time.Time // applies only when Wake starts a new turn
	// Internal continuations share Wake checks and the same turn delivery path.
	continuation string
	wrapUp       bool
	Schema       string
	// MessageID identifies the post already stored by the caller. A late or
	// repeated wake returns its delivery turn instead of starting an empty turn.
	MessageID string
}

// Wake makes a session read the posts stored for it in its room log. The
// caller stores the post first. A queued or running turn whose gate is open
// reads it as live steering; otherwise Wake starts a follow-up turn that
// resumes the exact harness session and drains the post into its prompt. It
// never waits for another turn. A closed gate returns ErrBusy; its pending
// posts are retried by the turn-end event after lock release.
func (m *Manager) Wake(ctx context.Context, ref string, opts WakeOptions) (WakeResult, error) {
	if err := authority.Check("subagent start", authority.Current(), nil); err != nil {
		return WakeResult{}, err
	}
	if opts.Schema != "" {
		if err := structuredoutput.CheckSchema(opts.Schema); err != nil {
			return WakeResult{}, err
		}
	}
	store := m.store()
	if err := ctx.Err(); err != nil {
		return WakeResult{}, err
	}
	info, err := store.DeliveryTarget(ref)
	if err != nil {
		return WakeResult{}, err
	}
	if info.Status == StatusClosed {
		return WakeResult{}, fmt.Errorf("%w: %s; start a new session", ErrClosed, info.ID)
	}
	if opts.RequireIdle && (info.Status == StatusQueued || info.Status == StatusRunning) {
		return WakeResult{}, ErrBusy
	}
	if m.RoomPaused(info.RoomID) {
		return WakeResult{Session: info.ID, Paused: true}, nil
	}
	if info.Consolidating != "" && !handoffPost(info.Meta, opts.MessageID) || info.Transfer.blocksTurns() {
		return WakeResult{}, errors.New("model transfer is in progress; posts remain stored for the replacement")
	}
	if !info.Resumable {
		return WakeResult{}, fmt.Errorf("session %s cannot take messages: profile %q's harness has no exact-session resume, so a follow-up turn or steering would silently start a fresh agent; start a new session instead", info.ID, info.Profile)
	}
	if opts.MessageID != "" {
		delivered, ok, err := store.deliveredPostTurn(info.ID, opts.MessageID)
		if err != nil {
			return WakeResult{}, err
		}
		if ok {
			return WakeResult{Session: info.ID, Turn: delivered.N, Steered: true}, nil
		}
	}
	if opts.MessageID != "" && len(info.InheritedInboxes) > 0 {
		msg, e := m.rooms().Message(info.RoomID, opts.MessageID)
		if e != nil {
			return WakeResult{}, e
		}
		if !msg.SessionAddressed(info.ID, info.Address()) {
			matched := false
			for _, in := range info.InheritedInboxes {
				if msg.SessionAddressed(in.Session, in.Address) {
					matched = true
				}
			}
			if matched {
				pending, e := store.inboxMessages(m.rooms(), info.Meta, nil)
				if e != nil {
					return WakeResult{}, e
				}
				eligible := false
				for _, post := range pending {
					if post.ID == opts.MessageID {
						eligible = true
						break
					}
				}
				if !eligible {
					return WakeResult{Session: info.ID, Turn: info.Turns, Steered: true}, nil
				}
			}
		}
	}
	if info.Status == StatusQueued || info.Status == StatusRunning {
		turn, open, err := store.gateOpen(info.ID)
		if err != nil {
			return WakeResult{}, err
		}
		if open {
			queued := info.Status == StatusQueued
			store.appendEvent(info.ID, map[string]any{"t": "steer_submitted", "turn": turn, "queued": queued})
			return WakeResult{Session: info.ID, Turn: turn, Steered: true, Queued: queued}, nil
		}
		return WakeResult{}, ErrBusy
	}
	// Idle. A record published before its first turn exists is still being
	// created, so its turn 1 is not the caller's to allocate.
	if info.Turns == 0 {
		return WakeResult{}, ErrBusy
	}
	// Without a harness session the first turn never started one, so the
	// next turn opens a fresh conversation instead of resuming.
	turn, delivered, err := m.beginTurnConfigured(info.ID, opts.continuation, opts.Schema, nil, opts.MessageID, "", opts.wrapUp, opts.Deadline)
	if errors.Is(err, errRoomPaused) {
		return WakeResult{Session: info.ID, Paused: true}, nil
	}
	if err != nil {
		return WakeResult{}, err
	}
	return WakeResult{Session: info.ID, Turn: turn.N, Steered: delivered}, nil
}

// Assign sends prompt as a follow-up turn of an idle session. Role services
// use it to give a kept session its next assignment with a result schema.
func (m *Manager) Assign(ctx context.Context, id, prompt, schema string, deadline time.Time) (Turn, error) {
	res, err := m.Wake(ctx, id, WakeOptions{continuation: prompt, Schema: schema, Deadline: deadline, RequireIdle: true})
	if err != nil {
		return Turn{}, err
	}
	if res.Paused {
		return Turn{}, errRoomPaused
	}
	return m.store().ReadTurn(res.Session, res.Turn)
}

// Wait blocks until the turn (0 = the session's current or latest) reaches a
// terminal status and returns it. There is deliberately no timeout: cancel ctx
// to stop waiting.
func (m *Manager) Wait(ctx context.Context, ref string, turn int) (Info, Turn, error) {
	store := m.store()
	info, err := store.Resolve(ref)
	if err != nil {
		return Info{}, Turn{}, err
	}
	if turn == 0 {
		turn = info.Turns
	}
	if turn <= 0 || turn > info.Turns {
		return info, Turn{}, fmt.Errorf("session %s has no turn %d (it has %d)", info.ID, turn, info.Turns)
	}
	for {
		t, err := store.ReadTurn(info.ID, turn)
		if err != nil {
			return info, Turn{}, err
		}
		if t.Done() && !store.lockHeld(info.ID) {
			info, _ = store.Info(info.ID)
			return info, t, nil
		}
		if !t.Done() {
			_, _ = store.Info(info.ID) // repairs a turn whose runner died
		}
		select {
		case <-ctx.Done():
			return info, t, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// NoRunningTurnError means a stop request found no active turn.
type NoRunningTurnError struct {
	SessionID string
	Status    string
}

func (e *NoRunningTurnError) Error() string {
	return fmt.Sprintf("session %s has no running turn (status %s)", e.SessionID, e.Status)
}

// Stop cancels the running turn; the session stays open and resumable. A
// stop by the session that receives its report records reason "recipient",
// so no report returns to that session.
func (m *Manager) Stop(ctx context.Context, ref string) (Turn, error) {
	reason := ""
	if cur, ok := mode.CurrentSession(); ok {
		if info, err := m.store().Resolve(ref); err == nil && m.receivesReport(info, cur.ID) {
			reason = "recipient"
		}
	}
	return m.stopSession(ctx, ref, "", reason)
}

// receivesReport reports whether session stopper receives the reports of
// child: its parent session, or the room's root session for a top-level child.
func (m *Manager) receivesReport(child Info, stopper string) bool {
	if child.ParentSession != "" {
		return child.ParentSession == stopper
	}
	root, ok, err := m.RootSession(child.RoomID)
	return err == nil && ok && root.ID == stopper
}

// stopSession permits only the owning coordinator to stop a reserved session.
func (m *Manager) stopSession(ctx context.Context, ref, reservation, reason string) (Turn, error) {
	if err := authority.Check("subagent stop", authority.Of(ctx), nil); err != nil {
		return Turn{}, err
	}
	store := m.store()
	info, err := store.Resolve(ref)
	if err != nil {
		return Turn{}, err
	}
	if info.Status != StatusRunning && info.Status != StatusQueued {
		return Turn{}, &NoRunningTurnError{SessionID: info.ID, Status: info.Status}
	}
	n := info.CurrentTurn
	var meta Meta
	var intentErr error
	err = store.withLockContext(ctx, info.ID, func() error {
		var e error
		meta, e = store.ReadMeta(info.ID)
		if e != nil {
			return e
		}
		if meta.Consolidating != "" && meta.Consolidating != reservation {
			return errors.New("cancel or finish consolidation before stopping this session")
		}
		if meta.Turns != n {
			return ErrBusy
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		// A failed intent write is reported after signaling: a corrupt record
		// must not prevent a signal from stopping its live runner.
		if turn, e := store.readTurnForUpdate(info.ID, n); e == nil {
			turn.StopAsked = true
			turn.StopReason = reason
			intentErr = store.writeTurn(info.ID, turn)
		} else {
			intentErr = e
		}
		return nil
	})
	if err != nil {
		return Turn{}, err
	}
	if err := m.signalTurn(meta, n, syscall.SIGTERM); err != nil {
		return Turn{}, errors.Join(intentErr, err)
	}
	if intentErr != nil {
		return Turn{}, fmt.Errorf("record the stop intent: %w", intentErr)
	}
	started := time.Now()
	for store.lockHeld(info.ID) {
		if err := ctx.Err(); err != nil {
			return Turn{}, err
		}
		if time.Since(started) > 25*time.Second {
			_ = m.signalTurn(meta, n, syscall.SIGKILL)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, _ = store.Info(info.ID) // repair a turn the runner could not finish
	t, err := store.ReadTurn(info.ID, n)
	return t, err
}

// Close ends the session: it stops a running turn and refuses further turns.
func (m *Manager) Close(ctx context.Context, ref string) (Info, error) {
	return m.CloseWithOptions(ctx, ref, CloseOptions{})
}

type CloseOptions struct {
	// DormantBefore requires an idle, unprotected session older than this cutoff.
	DormantBefore time.Time `json:"-"`
	closingToken  string
	// Force closes a session that owns responsibilities close cannot hand over.
	Force bool
	// KeepWorktrees locks the session's worktrees after its turn stops and
	// before the close commits, so a manual handoff keeps the source trees.
	KeepWorktrees bool
	// by is who closed the session, for its events: the caller, or the
	// engine for its own maintenance.
	by string
}

// TrackedPRs includes author, assigned reviewer and configured named reviewer
// links. Ended and untracked records remain visible as history.
func (m *Manager) TrackedPRs(ref string) ([]links.Link, error) {
	// A read resolves without repairing the session (item 4).
	info, err := m.store().ResolveSnapshot(ref)
	if err != nil {
		return nil, err
	}
	return links.ForSession(state.Root(filepath.Dir(m.store().root())), info.ID, info.RoomID)
}

func (m *Manager) CloseWithOptions(ctx context.Context, ref string, opts CloseOptions) (Info, error) {
	info, err := m.authorityTarget(ctx, "subagent close", ref)
	if err != nil {
		return Info{}, err
	}
	actor := authority.Of(ctx)
	opts.by = actor.Session
	if opts.by == "" {
		opts.by = string(actor.Role)
	}
	return m.closeSession(ctx, info, opts)
}

// CloseForEngine closes session id as the engine's own maintenance, not for
// a caller, so it checks no caller: notify replaces a root that cannot take
// posts, and the librarian replaces or retires its session. It is never an
// engine call (TestProtectedCallsAreBound). why says what the engine did;
// the session's events record "engine: <why>" as who closed it, never the
// owner.
func (m *Manager) CloseForEngine(ctx context.Context, id, why string, opts CloseOptions) (Info, error) {
	info, err := m.store().Resolve(id)
	if err != nil {
		return Info{}, err
	}
	opts.by = "engine: " + why
	return m.closeSession(ctx, info, opts)
}

// closeSession also handles children of an already authorized close.
func (m *Manager) closeSession(ctx context.Context, info Info, opts CloseOptions) (Info, error) {
	store := m.store()
	if info.Consolidating != "" {
		return info, errors.New("cancel or finish consolidation before closing this session")
	}
	if info.Status == StatusClosed {
		// The flag still has to do something on a repeated close: the trees may
		// outlive the close, and cleanup may not have reached them yet.
		if opts.KeepWorktrees {
			if err := m.keepWorktrees(ctx, info); err != nil {
				return info, err
			}
		}
		return info, m.withdrawClosedQuestions(info)
	}
	closedForCleanup := false
	defer func() {
		if closedForCleanup {
			_ = hooks.Emit(ctx, hooks.Event{Event: hooks.SessionFinish, Repo: info.Dir, Worktree: info.Dir, Session: info.Address(), Room: info.RoomID})
			m.startWorktreeCleanup(info, opts.KeepWorktrees)
		}
	}()
	// A standalone manager cannot move PR roles without the PR service.
	if !opts.Force && m.ClosePRs == nil {
		items, err := m.Responsibilities(ctx, info)
		if err != nil {
			return info, fmt.Errorf("responsibilities: cannot close: %w; use --force to close anyway", err)
		}
		items = slices.DeleteFunc(items, func(r Responsibility) bool { return r.Kind != "pr" })
		if len(items) > 0 {
			return info, fmt.Errorf("%w\n- PR handoff service is unavailable; use dyna subagent close %s --force to close anyway", responsibilityError("close", items), info.ID)
		}
	}
	// Root and the owner hold no goals, so a close that would leave one of them
	// as the heir must resolve each goal first. A forced close does not move the
	// goals to the owner either: root asks the owner, then resolves them.
	if err := m.refuseGoalsForTopHeir(info); err != nil {
		return info, err
	}
	if !opts.DormantBefore.IsZero() && (info.Status == StatusRunning || info.Status == StatusQueued) {
		return info, keptForWork("the session has an active or held turn")
	}
	if info.Status == StatusRunning || info.Status == StatusQueued {
		if m.checkpoint != nil {
			m.checkpoint("close-before-stop")
		}
		// The turn can finish after the status check. Only that typed
		// stop result permits close to continue; other errors still fail.
		var noRunningTurn *NoRunningTurnError
		if _, err := m.stopSession(ctx, info.ID, "", "close"); err != nil && !errors.As(err, &noRunningTurn) {
			return info, err
		}
	}
	// Fence new assignments before reading and moving existing work.
	if opts.DormantBefore.IsZero() {
		opts.closingToken = NewID()
		if err := m.reserveFinish(ctx, info.ID, opts.closingToken); err != nil {
			return info, err
		}
		defer m.releaseDormant(info.ID, opts.closingToken)
	}
	heir, err := m.closeHeir(info)
	if err != nil {
		return info, err
	}
	summary, err := m.handoffClose(ctx, info, heir)
	if err != nil {
		if !opts.Force {
			return info, fmt.Errorf("responsibilities: cannot close: %w; retry or use --force to close anyway", err)
		}
		summary = append(summary, "Handoff failed: "+err.Error())
	}
	// Serialize the final close with child creation and promotion. Children
	// created while Stop was in progress are included in this choice.
	unlock, err := lockNamesContext(ctx, store)
	if err != nil {
		return info, err
	}
	// Children that own nothing close after this session. They close after the
	// names lock is released, because each close takes it again.
	var cascade []string
	defer func() {
		if !closedForCleanup {
			return
		}
		for _, id := range cascade {
			child, err := store.Resolve(id)
			if err == nil {
				_, err = m.closeSession(ctx, child, CloseOptions{})
			}
			if err != nil {
				store.appendEvent(id, map[string]any{"t": "cascade_close_failed", "parent": info.ID, "error": err.Error()})
			}
		}
	}()
	defer unlock()
	fresh, err := store.ReadMeta(info.ID)
	if err != nil {
		return info, err
	}
	if fresh.Consolidating != "" {
		return info, errors.New("cancel or finish consolidation before closing this session")
	}
	if fresh.Closing == nil || fresh.Closing.Token != opts.closingToken || !fresh.Closing.Active() {
		if !opts.DormantBefore.IsZero() {
			return info, keptForWork("the close reservation changed or expired")
		}
		return info, errors.New("close reservation changed or expired; retry")
	}
	// Every check that can refuse this close has passed, and a running turn is
	// stopped, so the trees kept here are the ones cleanup would remove.
	if opts.KeepWorktrees {
		if err := m.keepWorktrees(ctx, info); err != nil {
			return info, err
		}
	}
	if !opts.DormantBefore.IsZero() {
		if opts.Force {
			return info, errors.New("dormant clear cannot force close")
		}
		if err := m.closeDormantLocked(ctx, info.ID, opts.DormantBefore, opts.closingToken); err != nil {
			return info, err
		}
	} else if _, err := store.updateMetaContext(ctx, info.ID, func(meta *Meta) { meta.Closed = time.Now() }); err != nil {
		return info, err
	}
	// Recheck the heir under the names lock: a bulk close may also close
	// the ancestor selected before the handoff.
	heir, err = m.closeHeir(Info{Meta: fresh})
	if err != nil {
		summary = append(summary, "Child heir lookup failed: "+err.Error())
	}
	// The session is closed now. A hand-over failure leaves that child open
	// under a closed parent, so record it and finish the close.
	var children []string
	if cascade, children, err = m.handOverChildren(ctx, fresh, heir); err != nil {
		store.appendEvent(info.ID, map[string]any{"t": "child_handover_failed", "error": err.Error()})
		summary = append(summary, "Child handoff failed: "+err.Error())
	}
	summary = append(summary, children...)
	for _, key := range []string{"session/" + info.ID, info.ID} {
		if err := sessionhost.CloseKey(ctx, key); err != nil {
			summary = append(summary, "Native process cleanup failed: "+err.Error())
		}
	}
	destroyComputer(info.ID)
	closedForCleanup = true
	if err := m.withdrawClosedQuestions(info); err != nil {
		return info, err
	}
	m.closeGoals(info, heir.ID)
	store.appendEvent(info.ID, map[string]any{"t": "session_closed", "by": opts.by, "handoff": summary})
	closed, err := store.Info(info.ID)
	closed.CloseSummary = summary
	return closed, err
}

// ExecSpawner runs each turn in a detached `dyna subagent _turn` process.
type ExecSpawner struct{}

func (ExecSpawner) Spawn(m *Manager, id string, turn int, lock *os.File) error {
	dir, err := m.store().Dir(id)
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(m.executable(), "subagent", "_turn", id, strconv.Itoa(turn))
	cmd.ExtraFiles = []*os.File{lock} // fd 3 keeps the flock alive in the runner
	cmd.Env = append(childenv.WithoutLocks(os.Environ()), TurnLockFDEnv+"=3")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	launch := callerid.Launch{DataDir: filepath.Dir(m.store().root()), Name: id, Actor: callerid.Identity{Kind: callerid.Session, Session: id}, Launcher: "turn"}
	if err := callerid.Start(context.Background(), cmd, launch); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	_ = lock.Close() // the child holds its own descriptor of the same lock
	return nil
}

// TurnLockFDEnv tells `dyna subagent _turn` which inherited fd holds the lock.
const TurnLockFDEnv = "DYNA_SUBAGENT_LOCK_FD"

// InProcessSpawner runs turns on goroutines (tests, embedding).
// Runners, when set, counts running turns, so a test can wait for every
// runner to stop before it removes the data dir.
type InProcessSpawner struct {
	Ctx     context.Context
	Runners *sync.WaitGroup
}

func (s InProcessSpawner) Spawn(m *Manager, id string, turn int, lock *os.File) error {
	ctx := s.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if s.Runners != nil {
		s.Runners.Add(1)
	}
	go func() {
		if s.Runners != nil {
			defer s.Runners.Done()
		}
		_ = m.RunTurn(ctx, id, turn, lock)
	}()
	return nil
}
