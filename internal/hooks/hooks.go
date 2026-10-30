// Package hooks stores trusted shell hooks outside repositories and runs them
// directly. Hook documents and runtime state have separate store locks.
// Neither lock is held while a hook command runs or an owner notice is posted.
package hooks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/scratch"
)

const (
	WorktreeCreated = "worktree-created"
	BeforeLand      = "before-land"
	AfterLand       = "after-land"
	PRMerged        = "pr-merged"
	MainUpdated     = "main-updated"
	SessionFinish   = "session-finish"
	Schedule        = "schedule"
	// SessionTurnEnded fires when a bound session's turn ends, so a job that
	// reacts to a turn can start at once instead of at its next interval.
	SessionTurnEnded = "session-turn-ended"
)

type Hook struct {
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Event   string `json:"event"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Timeout string `json:"timeout"`
	OnFail  string `json:"onFail"`
	// Scope says where the hook belongs: one repository, or the machine. A
	// hook stored before scope existed belongs to its repository.
	Scope Scope `json:"scope"`
	// Builtin names the table entry a hook the engine seeded runs, and is empty
	// for a hook the owner added. The engine fixes its command and cannot
	// remove it.
	Builtin      Builtin    `json:"builtin,omitempty"`
	AddedBy      string     `json:"addedBy"`
	AddedAt      time.Time  `json:"addedAt"`
	EditedBy     string     `json:"editedBy,omitempty"`
	EditedAt     *time.Time `json:"editedAt,omitempty"`
	EveryMinutes int        `json:"everyMinutes,omitempty"`
	// Disabled keeps the hook's settings and stops automatic runs. It is
	// absent in older hook documents, where the hook runs.
	Disabled bool `json:"disabled,omitempty"`
}

type Event struct {
	Event    string `json:"event"`
	Repo     string `json:"repo"`
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	SHA      string `json:"sha,omitempty"`
	PR       string `json:"pr,omitempty"`
	Session  string `json:"session,omitempty"`
	Room     string `json:"room,omitempty"`
	Name     string `json:"name,omitempty"`
	Snapshot bool   `json:"snapshot,omitempty"`
}

type document struct {
	Repo  string `json:"repo,omitempty"`
	Hooks []Hook `json:"hooks"`
}
type state struct {
	Main string `json:"main,omitempty"`
	// Fetched holds the last origin fetch of each local checkout, keyed by its
	// root. Clones of one remote share a repository ID, so each clone needs its own.
	Fetched map[string]time.Time `json:"fetched,omitempty"`
	Due     map[string]time.Time `json:"due,omitempty"`
	// Every is the interval that set each Due entry, so an edited interval
	// can move the pending run instead of leaving the old deadline.
	Every   map[string]int  `json:"every,omitempty"`
	Notices map[string]bool `json:"notices,omitempty"`
}

type Service struct {
	// DataDir and Exec are injectable for isolated tests.
	DataDir string
	Exec    processguard.Executor
	Install *hookinstall.Service
	// Builtins is the table of automatic hooks this service seeds and runs.
	// internal/automations holds the table; a service without one runs no
	// automatic hook.
	Builtins []BuiltinSpec
	// Post is where a run's events go. Tests replace it; the default posts
	// them through room.PostEvent.
	Post func(Event, room.HookEvent) error
	// Fetch refreshes the remote default branch; tests replace it with a
	// local fake.
	Fetch func(context.Context, string) error
	// Checkouts lists the room checkouts that follow their default branch; tests
	// replace it. The default reads the repositories of every room.
	Checkouts func(context.Context) ([]string, error)
}

// New returns a hook service that seeds and runs the given automatic hooks.
// A caller that needs none, such as a spool drain, passes none.
func New(builtins ...BuiltinSpec) Service { return Service{Builtins: builtins} }

// scheduleKey names one hook's schedule in the runtime state. A hook that is
// removed and added again starts with no run time, because AddedAt changes.
func scheduleKey(h Hook) string { return h.Name + ":" + h.AddedAt.Format(time.RFC3339Nano) }
func (s Service) dir(id string) string {
	return filepath.Join(s.dataDir(), "repos", fmt.Sprintf("%x", sha256.Sum256([]byte(id))))
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func Validate(h Hook) error {
	if !namePattern.MatchString(h.Name) {
		return fmt.Errorf("hook name must use 1 to 64 letters, digits, underscores, or hyphens")
	}
	switch h.Scope {
	case ScopeRepository, ScopeGlobal:
	case "":
		// A hook stored before scope existed is a repository hook.
		h.Scope = ScopeRepository
	default:
		return fmt.Errorf("unknown hook scope %q", h.Scope)
	}
	// A built-in runs in the engine, so its command and directory come from
	// the built-in table and are never a shell command. Its schedule and
	// timeout are still the owner's to set, so both are checked here.
	if h.Builtin != "" {
		if h.Scope != ScopeGlobal {
			return fmt.Errorf("a built-in hook belongs to no repository")
		}
		if h.EveryMinutes < 1 {
			return fmt.Errorf("schedule requires a positive every-minutes interval")
		}
		timeout, err := time.ParseDuration(h.Timeout)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		return nil
	}
	switch h.Event {
	case WorktreeCreated, BeforeLand, AfterLand, PRMerged, MainUpdated, SessionFinish, Schedule, SessionTurnEnded:
	default:
		return fmt.Errorf("unknown hook event %q", h.Event)
	}
	if h.Command == "" {
		return fmt.Errorf("hook command is required")
	}
	if h.Cwd != "worktree" && h.Cwd != "main" && !filepath.IsAbs(h.Cwd) {
		return fmt.Errorf("cwd must be worktree, main, or an absolute path")
	}
	if h.Scope == ScopeGlobal && (h.Cwd == "worktree" || h.Cwd == "main") {
		return fmt.Errorf("a global hook has no repository, so cwd must be an absolute path")
	}
	if timeout, err := time.ParseDuration(h.Timeout); err != nil || timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if h.OnFail != "alert" && !(h.OnFail == "block" && h.Event == BeforeLand) {
		return fmt.Errorf("onFail must be alert; block is allowed only for before-land")
	}
	if h.Event == Schedule && h.EveryMinutes < 1 {
		return fmt.Errorf("schedule requires a positive every-minutes interval")
	}
	return nil
}

// List returns a repository's hooks in their saved order.
func (s Service) List(ctx context.Context, repo string) ([]Hook, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return s.load(ctx, r.ID)
}

// ListGlobal returns the global hooks: the built-ins the engine seeds and the
// hooks the owner added for the whole machine.
func (s Service) ListGlobal(ctx context.Context) ([]Hook, error) {
	return s.load(ctx, globalKey)
}

func (s Service) Add(ctx context.Context, repo string, h Hook) error {
	h.Scope = ScopeRepository
	return s.add(ctx, repo, h)
}

// AddGlobal stores a hook that belongs to the machine and runs even when no
// checkout is open. It has no repository, so its directory must be absolute.
func (s Service) AddGlobal(ctx context.Context, h Hook) error {
	h.Scope = ScopeGlobal
	return s.add(ctx, "", h)
}

func (s Service) add(ctx context.Context, repo string, h Hook) error {
	if err := authority.Check("git hooks add", authority.Of(ctx), nil); err != nil {
		return err
	}
	if h.Builtin != "" {
		return fmt.Errorf("hook %q is built in, so the engine seeds it", h.Name)
	}
	// Only a global hook can collide with a built-in row: a built-in belongs
	// to the machine, so a repository hook may use the same name.
	if h.Scope == ScopeGlobal {
		if _, reserved := s.builtinSpec(Builtin(h.Name)); reserved {
			return fmt.Errorf("hook name %q is reserved for a built-in hook", h.Name)
		}
	}
	if err := Validate(h); err != nil {
		return err
	}
	if h.AddedBy == "" {
		h.AddedBy = "owner"
	}
	h.AddedAt = time.Now().UTC()
	id, main := globalKey, ""
	if repo != "" {
		r, err := repoid.Resolve(ctx, repo)
		if err != nil {
			return err
		}
		id, main = r.ID, r.Main
	}
	return s.update(ctx, id, func(d *document) error {
		for _, old := range d.Hooks {
			if old.Name == h.Name {
				return fmt.Errorf("hook %q already exists; remove it first", h.Name)
			}
		}
		if main != "" {
			d.Repo = main
		}
		d.Hooks = append(d.Hooks, h)
		return nil
	})
}

// Patch changes only fields that the caller supplies.
type Patch struct {
	Command, Event, Cwd, Timeout, OnFail *string
	EveryMinutes                         *int
	Disabled                             *bool
}

// Edit changes the supplied fields of a repository hook. An empty repository
// edits a global hook.
func (s Service) Edit(ctx context.Context, repo, name, editor string, patch Patch) error {
	if err := authority.Check("git hooks edit", authority.Of(ctx), nil); err != nil {
		return err
	}

	id := globalKey
	if repo != "" {
		r, err := repoid.Resolve(ctx, repo)
		if err != nil {
			return err
		}
		id = r.ID
	}
	// A run time written by an older release has no record of the interval
	// that set it. Record the interval that is in force before it changes, so
	// the next tick moves the pending run instead of firing its old deadline.
	if patch.EveryMinutes != nil {
		hooks, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		for _, old := range hooks {
			if old.Name != name || old.EveryMinutes == *patch.EveryMinutes {
				continue
			}
			key := scheduleKey(old)
			if err := s.runtime(id, func(st *state) error {
				if _, known := st.Every[key]; !known && !st.Due[key].IsZero() {
					st.Every[key] = old.EveryMinutes
				}
				return nil
			}); err != nil {
				return err
			}
			break
		}
	}
	return s.update(ctx, id, func(d *document) error {
		for i, old := range d.Hooks {
			if old.Name != name {
				continue
			}
			// A built-in's action comes from the built-in table, so only its
			// interval, timeout and switch can change.
			if old.Builtin != "" && (patch.Command != nil || patch.Event != nil || patch.Cwd != nil || patch.OnFail != nil) {
				return fmt.Errorf("hook %q is built in, so its action cannot change; edit its interval, timeout or switch", name)
			}
			h := old
			if patch.Command != nil {
				h.Command = *patch.Command
			}
			if patch.Event != nil {
				h.Event = *patch.Event
			}
			if patch.Cwd != nil {
				h.Cwd = *patch.Cwd
			}
			if patch.Timeout != nil {
				h.Timeout = *patch.Timeout
			}
			if patch.OnFail != nil {
				h.OnFail = *patch.OnFail
			}
			if patch.EveryMinutes != nil {
				h.EveryMinutes = *patch.EveryMinutes
			}
			if patch.Disabled != nil {
				h.Disabled = *patch.Disabled
			}
			if err := Validate(h); err != nil {
				return err
			}
			if editor == "" {
				editor = "owner"
			}
			now := time.Now().UTC()
			h.EditedBy, h.EditedAt = editor, &now
			d.Hooks[i] = h
			return nil
		}
		return fmt.Errorf("unknown hook %q", name)
	})
}

// Remove deletes a repository hook, or a global one when repo is empty. A
// built-in stays, because the engine seeds it and its switch turns it off.
func (s Service) Remove(ctx context.Context, repo, name string) error {
	if err := authority.Check("git hooks rm", authority.Of(ctx), nil); err != nil {
		return err
	}

	id := globalKey
	if repo != "" {
		r, err := repoid.Resolve(ctx, repo)
		if err != nil {
			return err
		}
		id = r.ID
	}
	return s.update(ctx, id, func(d *document) error {
		for i, h := range d.Hooks {
			if h.Name == name {
				if h.Builtin != "" {
					return fmt.Errorf("hook %q is built in, so it cannot be removed; turn it off instead", name)
				}
				d.Hooks = append(d.Hooks[:i], d.Hooks[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("unknown hook %q", name)
	})
}

// Runs lists the stored runs of one hook. An empty repository lists the runs
// of a global hook.
func (s Service) Runs(ctx context.Context, repo, name string) ([]Run, error) {
	if repo == "" {
		return s.runs(ctx, globalKey, name)
	}
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return s.runs(ctx, r.ID, name)
}

// alert posts a failed run's result for an alert hook. The first failure of a
// hook, command and cause may create the repo's room for the owner; every run
// still ends its own card, quietly, so a retried failure never leaves one
// running. A passing run clears the claim in clearNotices, so a later failure
// is new news and may reach the owner again.
func (s Service) alert(r repoid.Repo, e Event, h Hook, run Run, result room.HookState, cause, detail string) {
	ev := hookEvent(e, h, run, result, detail)
	key := noticePrefix(h) + cause
	claimed := false
	if err := s.runtime(r.ID, func(st *state) error {
		if !st.Notices[key] {
			st.Notices[key] = true
			claimed = true
		}
		return nil
	}); err != nil || !claimed {
		s.emit(e, ev)
		return
	}
	if err := s.postEvent(e, ev, true); err != nil {
		_ = s.runtime(r.ID, func(st *state) error { delete(st.Notices, key); return nil })
	}
}

// emit posts one routine run event: a start, or a result that passed. It
// never fails a run, which is already stored, and it never creates a room.
func (s Service) emit(e Event, ev room.HookEvent) {
	_ = s.postEvent(e, ev, false)
}

// postEvent posts one run event. Post is the test seam; the default writes
// through room.PostEvent.
func (s Service) postEvent(e Event, ev room.HookEvent, create bool) error {
	if s.Post != nil {
		return s.Post(e, ev)
	}
	return postRoomEvent(e, ev, create)
}

// postRoomEvent posts a run event to the event's room, else to the repo's
// room. A routine event never creates a room, so a repo that is nobody's room
// stays quiet; a failed hook creates the repo's room, as its alert always has.
func postRoomEvent(e Event, ev room.HookEvent, create bool) error {
	// A global hook belongs to no repository, so a run of it has no room to
	// post in. Its runs stay in the hook store for the Hooks screen.
	if e.Repo == "" {
		return nil
	}
	rooms := room.DefaultStore()
	var rec room.Record
	var found bool
	var err error
	if e.Room != "" {
		rec, err = rooms.Resolve(e.Room)
		found = err == nil
	} else {
		root, rootErr := room.RepoRoot(e.Repo)
		if rootErr != nil {
			return rootErr
		}
		rec, found, err = rooms.RepoRoom(root)
	}
	if err != nil {
		return err
	}
	if !found {
		if !create {
			return nil
		}
		if rec, err = rooms.EnsureRepoRoom(e.Repo, "hooks"); err != nil {
			return err
		}
	}
	_, err = rooms.PostEvent(rec.ID, ev, rec.Name)
	return err
}

// spoolAlert tells the owner that a queued hook never reached the spool. It
// stores the failure as a run, so the card opens a log that holds the cause.
// When even that record cannot be stored, the spool error itself is still on
// its way to the caller, and this line names why no card followed it.
func (s Service) spoolAlert(ctx context.Context, r repoid.Repo, e Event, h Hook, cause error) {
	run, file, err := s.newRun(ctx, r, e, h)
	if err != nil {
		log.Printf("hook spool alert for %s: cannot record the run: %v (spool error: %v)", h.Name, err, cause)
		return
	}
	run.Result = "error"
	run.FinishedAt = time.Now().UTC()
	_, _ = fmt.Fprintln(file, "spool write error:", cause)
	_ = file.Close()
	if _, err := s.saveRun(ctx, run); err != nil {
		log.Printf("hook spool alert for %s: cannot store the run: %v (spool error: %v)", h.Name, err, cause)
		return
	}
	s.alert(r, e, h, run, room.HookError, "spool write error", "spool write error: "+cause.Error())
}

// hookEvent describes one run of a hook for the room. The full command can
// hold a secret, so the room sees its first word only.
func hookEvent(e Event, h Hook, run Run, state room.HookState, detail string) room.HookEvent {
	ev := room.HookEvent{
		RunID: run.ID, Repo: e.Repo, Hook: h.Name, Event: e.Event,
		Label: h.Label, Command: commandLabel(h.Command), State: state, Detail: detail,
		Builtin: h.Builtin != "",
	}
	if state != room.HookRunning && !run.StartedAt.IsZero() && !run.FinishedAt.IsZero() {
		ev.DurationMS = int(run.FinishedAt.Sub(run.StartedAt).Milliseconds())
	}
	return ev
}

// resultState names how a failed run ended: an exit code is a failed hook, and
// a timeout, a cancellation or a runner fault is an error.
func resultState(run Run) room.HookState {
	if run.Result == "fail" {
		return room.HookFailed
	}
	return room.HookError
}

var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// commandLabel is the first word of a hook command, so a card can name the
// program without carrying the arguments, which may hold a secret.
func commandLabel(command string) string {
	for _, word := range strings.Fields(command) {
		if envAssignment.MatchString(word) {
			continue
		}
		word = strings.Trim(word, `"'`)
		// Cut by rune, so a word outside ASCII stays valid UTF-8.
		if runes := []rune(word); len(runes) > 24 {
			word = string(runes[:24])
		}
		return word
	}
	return ""
}

// Run executes one named hook now. Event values supplied by the emitter win
// over sampled Git values.
func (s Service) Run(ctx context.Context, e Event, name string) (Run, error) {
	return s.run(ctx, e, name, nil, true)
}

// RunGlobal runs one named global hook now, whether the owner added it or the
// engine seeded it.
func (s Service) RunGlobal(ctx context.Context, e Event, name string) (Run, error) {
	e.Repo = ""
	return s.run(ctx, e, name, nil, true)
}

// run records preparationErr without executing a command, or executes the hook
// when preparation succeeded. An event that names no repository runs a global
// hook.
func (s Service) run(ctx context.Context, e Event, name string, preparationErr error, requested bool) (Run, error) {

	r := repoid.Repo{ID: globalKey}
	if e.Repo != "" {
		resolved, err := repoid.Resolve(ctx, e.Repo)
		if err != nil {
			return Run{}, err
		}
		r = resolved
	}
	hooks, err := s.load(ctx, r.ID)
	if err != nil {
		return Run{}, err
	}
	for _, h := range hooks {
		if h.Name != name {
			continue
		}
		// Reloaded settings can change while an earlier hook is running.
		if h.Disabled && !requested {
			return Run{}, nil
		}
		if err := Validate(h); err != nil {
			return Run{}, err
		}
		if h.Builtin != "" {
			return s.runBuiltin(ctx, r, h, e)
		}
		if e.Event == "" {
			e.Event = h.Event
		}
		if e.Worktree == "" {
			e.Worktree = r.Root
		}
		// A global hook has no checkout, so it samples no Git values.
		if e.SHA == "" && preparationErr == nil && e.Worktree != "" {
			e.SHA, _ = repoid.Git(ctx, e.Worktree, "rev-parse", "HEAD")
		}
		if e.Branch == "" && preparationErr == nil && e.Worktree != "" {
			e.Branch, _ = repoid.Git(ctx, e.Worktree, "symbolic-ref", "--short", "-q", "HEAD")
		}
		cwd := h.Cwd
		if cwd == "worktree" {
			cwd = e.Worktree
		}
		if cwd == "main" {
			cwd = r.Main
		}
		ev, log, err := s.newRun(ctx, r, e, h)
		if err != nil {
			return Run{}, err
		}
		// The card shows the run as running before the command starts, so a
		// long hook is never invisible. A scheduled run stays quiet until it fails,
		// so an every-minute hook does not fill the feed.
		if e.Event != Schedule {
			s.emit(e, hookEvent(e, h, ev, room.HookRunning, ""))
		}
		env := append(processguard.MetadataEnv(), "DYNA_HOOK_EVENT="+e.Event, "DYNA_HOOK_REPO="+r.ID, "DYNA_HOOK_WORKTREE="+e.Worktree, "DYNA_HOOK_BRANCH="+e.Branch, "DYNA_HOOK_SHA="+e.SHA, "DYNA_HOOK_PR="+e.PR, "DYNA_HOOK_SESSION="+e.Session, "DYNA_HOOK_RUN_ID="+ev.ID, "DYNA_HOOK_LOG="+ev.LogPath)
		tempEnv, removeTemp := scratch.NewRunTemp()
		defer removeTemp()
		env = append(env, tempEnv...)
		timeout, _ := time.ParseDuration(h.Timeout)
		runErr := preparationErr
		if runErr == nil {
			runErr = validateHookMetadata(env)
		}
		if runErr == nil {
			runErr = s.execute()(ctx, processguard.Process{
				Dir: cwd, Args: []string{"nice", "-n", "10", "sh", "-c", h.Command},
				Env: env, Timeout: timeout, Output: log,
				Bind: s.bindHook(h.Name, requested),
			})
		}
		if runErr != nil {
			_, logErr := fmt.Fprintln(log, runErr)
			runErr = errors.Join(runErr, logErr)
		}
		closeErr := log.Close()
		ev.Result = "pass"
		if runErr != nil {
			ev.Result = "error"
			var exit *exec.ExitError
			if preparationErr == nil && errors.As(runErr, &exit) {
				ev.Result = "fail"
			}
		}
		ev.FinishedAt = time.Now().UTC()
		_, saveErr := s.saveRun(ctx, ev)
		runErr = errors.Join(runErr, closeErr, saveErr)
		// A recipe can hand its result to an independent installation unit.
		if runErr == nil {
			ev, runErr = s.pendingInstall(ctx, ev)
		}
		// Every started run posts its result, so no card stays running. A failed
		// run of an alert hook also reaches the owner: the claim there covers the
		// repo room the first failure may create, not the run's own card. A
		// handoff to an install unit posts its result when that unit finishes.
		if runErr != nil {
			cause := failureClass(runErr)
			if preparationErr != nil {
				cause = "snapshot"
			}
			if h.OnFail == "alert" {
				s.alert(r, e, h, ev, resultState(ev), cause, cause)
			} else {
				s.emit(e, hookEvent(e, h, ev, resultState(ev), cause))
			}
		} else if ev.Result != "pending" && e.Event != Schedule {
			s.emit(e, hookEvent(e, h, ev, room.HookPassed, ""))
		}
		if runErr == nil && ev.Result != "pending" {
			s.clearNotices(r.ID, h)
		}
		return ev, runErr
	}
	return Run{}, fmt.Errorf("unknown hook %q", name)
}

func noticePrefix(h Hook) string {
	return fmt.Sprintf("%x:", sha256.Sum256([]byte(h.Name+"\x00"+h.Command)))
}
func failureClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit %d", exit.ExitCode())
	}
	return "runner error"
}

// runBuiltin runs one automatic hook in the engine process. It keeps the run
// record, the log and the run events of a shell hook, so the screen and the
// feed show both the same way. id is the repository identity, or the global
// key for a built-in that belongs to no repository.
func (s Service) runBuiltin(ctx context.Context, r repoid.Repo, h Hook, e Event) (Run, error) {
	spec, err := s.builtin(h)
	if err != nil {
		return Run{}, err
	}
	if e.Event == "" {
		e.Event = h.Event
	}
	ev, log, err := s.newRun(ctx, r, e, h)
	if err != nil {
		return Run{}, err
	}
	// The card shows the run as running before the pass starts, so a slow pass
	// is never invisible.
	s.emit(e, hookEvent(e, h, ev, room.HookRunning, ""))
	timeout, _ := time.ParseDuration(h.Timeout)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runErr := spec.Run(runCtx, h.EveryMinutes, log)
	if runErr != nil {
		_, logErr := fmt.Fprintln(log, runErr)
		runErr = errors.Join(runErr, logErr)
	}
	closeErr := log.Close()
	ev.Result = "pass"
	if runErr != nil {
		ev.Result = "error"
	}
	ev.FinishedAt = time.Now().UTC()
	_, saveErr := s.saveRun(ctx, ev)
	runErr = errors.Join(runErr, closeErr, saveErr)
	if runErr != nil {
		s.emit(e, hookEvent(e, h, ev, resultState(ev), failureClass(runErr)))
	} else {
		s.emit(e, hookEvent(e, h, ev, room.HookPassed, ""))
	}
	return ev, runErr
}
