// Package hooks stores trusted shell hooks outside repositories and runs them
// directly. Hook documents and runtime state have separate store locks.
// Neither lock is held while a hook command runs or an owner notice is posted.
package hooks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shellquote"
)

const (
	WorktreeCreated = "worktree-created"
	BeforeLand      = "before-land"
	AfterLand       = "after-land"
	PRMerged        = "pr-merged"
	MainUpdated     = "main-updated"
	SessionFinish   = "session-finish"
	Schedule        = "schedule"
)

type Hook struct {
	Name         string     `json:"name"`
	Label        string     `json:"label,omitempty"`
	Event        string     `json:"event"`
	Command      string     `json:"command"`
	Cwd          string     `json:"cwd"`
	Timeout      string     `json:"timeout"`
	OnFail       string     `json:"onFail"`
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
	Main      string               `json:"main,omitempty"`
	FetchedAt time.Time            `json:"fetchedAt,omitempty"`
	Due       map[string]time.Time `json:"due,omitempty"`
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
	Notify  func(Event, string) error
	// Fetch refreshes origin/main; tests replace it with a local fake.
	Fetch func(context.Context, string) error
}

func New() Service { return Service{} }

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
	switch h.Event {
	case WorktreeCreated, BeforeLand, AfterLand, PRMerged, MainUpdated, SessionFinish, Schedule:
	default:
		return fmt.Errorf("unknown hook event %q", h.Event)
	}
	if h.Command == "" {
		return fmt.Errorf("hook command is required")
	}
	if h.Cwd != "worktree" && h.Cwd != "main" && !filepath.IsAbs(h.Cwd) {
		return fmt.Errorf("cwd must be worktree, main, or an absolute path")
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
func (s Service) List(ctx context.Context, repo string) ([]Hook, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return s.load(ctx, r.ID)
}

func (s Service) Add(ctx context.Context, repo string, h Hook) error {
	if err := authority.Check("git hooks add", authority.Of(ctx), nil); err != nil {
		return err
	}

	if err := Validate(h); err != nil {
		return err
	}
	if h.AddedBy == "" {
		h.AddedBy = "owner"
	}
	h.AddedAt = time.Now().UTC()
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return err
	}
	return s.update(ctx, r.ID, func(d *document) error {
		for _, old := range d.Hooks {
			if old.Name == h.Name {
				return fmt.Errorf("hook %q already exists; remove it first", h.Name)
			}
		}
		d.Repo = r.Main
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

func (s Service) Edit(ctx context.Context, repo, name, editor string, patch Patch) error {
	if err := authority.Check("git hooks edit", authority.Of(ctx), nil); err != nil {
		return err
	}

	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return err
	}
	// A run time written by an older release has no record of the interval
	// that set it. Record the interval that is in force before it changes, so
	// the next tick moves the pending run instead of firing its old deadline.
	if patch.EveryMinutes != nil {
		hooks, err := s.load(ctx, r.ID)
		if err != nil {
			return err
		}
		for _, old := range hooks {
			if old.Name != name || old.EveryMinutes == *patch.EveryMinutes {
				continue
			}
			key := scheduleKey(old)
			if err := s.runtime(r.ID, func(st *state) error {
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
	return s.update(ctx, r.ID, func(d *document) error {
		for i, old := range d.Hooks {
			if old.Name != name {
				continue
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

func (s Service) Remove(ctx context.Context, repo, name string) error {
	if err := authority.Check("git hooks rm", authority.Of(ctx), nil); err != nil {
		return err
	}

	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return err
	}
	return s.update(ctx, r.ID, func(d *document) error {
		for i, h := range d.Hooks {
			if h.Name == name {
				d.Hooks = append(d.Hooks[:i], d.Hooks[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("unknown hook %q", name)
	})
}
func (s Service) Runs(ctx context.Context, repo, name string) ([]Run, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return s.runs(ctx, r.ID, name)
}
func (s Service) notice(r repoid.Repo, e Event, h Hook, cause string) {
	key := noticePrefix(h) + cause
	claimed := false
	if err := s.runtime(r.ID, func(st *state) error {
		if !st.Notices[key] {
			st.Notices[key] = true
			claimed = true
		}
		return nil
	}); err != nil || !claimed {
		return
	}
	text := fmt.Sprintf("Hook %s failed for %s: %s. Read its log with `dyna git hooks log %s --repo %s`.", h.Name, e.Event, cause, h.Name, shellquote.Quote(r.Main))
	notify := s.Notify
	if notify == nil {
		notify = ownerNotice
	}
	if err := notify(e, text); err != nil {
		_ = s.runtime(r.ID, func(st *state) error { delete(st.Notices, key); return nil })
	}
}

// ownerNotice posts to the event's room, else to the repo's room, which it
// creates on first use.
func ownerNotice(e Event, text string) error {
	rooms := room.DefaultStore()
	var rec room.Record
	var err error
	if e.Room != "" {
		rec, err = rooms.Resolve(e.Room)
	} else {
		rec, err = rooms.EnsureRepoRoom(e.Repo, "hooks")
	}
	if err != nil {
		return err
	}
	_, err = rooms.Post(rec.ID, room.PostInput{From: "hooks", To: []string{rec.Name}, Kind: "hook-failed", Text: text})
	return err
}

// Run executes one named hook now. Event values supplied by the emitter win
// over sampled Git values.
func (s Service) Run(ctx context.Context, e Event, name string) (Run, error) {
	return s.run(ctx, e, name, nil, true)
}

// run records preparationErr without executing a command, or executes the hook
// when preparation succeeded.
func (s Service) run(ctx context.Context, e Event, name string, preparationErr error, requested bool) (Run, error) {

	r, err := repoid.Resolve(ctx, e.Repo)
	if err != nil {
		return Run{}, err
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
		if e.Event == "" {
			e.Event = h.Event
		}
		if e.Worktree == "" {
			e.Worktree = r.Root
		}
		if e.SHA == "" && preparationErr == nil {
			e.SHA, _ = repoid.Git(ctx, e.Worktree, "rev-parse", "HEAD")
		}
		if e.Branch == "" && preparationErr == nil {
			e.Branch, _ = repoid.Git(ctx, e.Worktree, "symbolic-ref", "--short", "-q", "HEAD")
		}
		cwd := h.Cwd
		if cwd == "worktree" {
			cwd = e.Worktree
		}
		if cwd == "main" {
			cwd = r.Main
		}
		ev, log, err := s.newRun(ctx, r.ID, h, e.Session)
		if err != nil {
			return Run{}, err
		}
		env := append(processguard.MetadataEnv(), "DYNA_HOOK_EVENT="+e.Event, "DYNA_HOOK_REPO="+r.ID, "DYNA_HOOK_WORKTREE="+e.Worktree, "DYNA_HOOK_BRANCH="+e.Branch, "DYNA_HOOK_SHA="+e.SHA, "DYNA_HOOK_PR="+e.PR, "DYNA_HOOK_SESSION="+e.Session, "DYNA_HOOK_RUN_ID="+ev.ID, "DYNA_HOOK_LOG="+ev.LogPath)
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
		// Alert only after the result and log are durable.
		if runErr != nil && h.OnFail == "alert" {
			cause := failureClass(runErr)
			if preparationErr != nil {
				cause = "snapshot"
			}
			s.notice(r, e, h, cause)
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
