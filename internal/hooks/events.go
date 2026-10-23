package hooks

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/shellquote"
)

// BlockError is the only hook error that may prevent a lifecycle operation.
type BlockError struct{ Err error }

func (e *BlockError) Error() string { return e.Err.Error() }
func (e *BlockError) Unwrap() error { return e.Err }

// Emit runs blocking before-land hooks synchronously. Other hooks wait in
// an on-disk spool drained by the engine. They wait while the engine is down.
// Callers may ignore errors except for before-land.
func Emit(ctx context.Context, e Event) error { return New().Emit(ctx, e) }
func (s Service) Emit(ctx context.Context, e Event) error {

	r, err := repoid.Resolve(ctx, e.Repo)
	if err != nil {
		if e.Event == BeforeLand {
			return &BlockError{Err: err}
		}
		return err
	}
	e.Repo = r.Root
	hooks, err := s.load(ctx, r.ID)
	if err != nil {
		if e.Event == BeforeLand {
			return &BlockError{Err: err}
		}
		return err
	}
	async := false
	for _, h := range hooks {
		if h.Event != e.Event || h.Disabled || (e.Name != "" && h.Name != e.Name) {
			continue
		}
		if err := Validate(h); err != nil {
			if e.Event == BeforeLand {
				return &BlockError{Err: fmt.Errorf("hook %s is invalid: %w. Inspect it with dyna git hooks list --repo %s; read its log with `dyna git hooks log %s --repo %s`", h.Name, err, shellquote.Quote(r.Main), h.Name, shellquote.Quote(r.Main))}
			}
			return err
		}
		if h.OnFail == "block" && e.Event == BeforeLand {
			if _, err := s.run(ctx, e, h.Name, nil, false); err != nil {
				return &BlockError{Err: fmt.Errorf("hook %s blocked landing: %w. Read its log with `dyna git hooks log %s --repo %s`", h.Name, err, h.Name, shellquote.Quote(r.Main))}
			}
		} else {
			async = true
		}
	}
	if !async {
		return nil
	}
	if err := s.Enqueue(e); err != nil {
		for _, h := range hooks {
			if h.Event == e.Event && h.OnFail == "alert" && !h.Disabled {
				s.spoolAlert(ctx, r, e, h, err)
			}
		}
		return err
	}
	return nil
}

// Dispatch runs nonblocking hooks for one event. It does not rerun block hooks.
func (s Service) Dispatch(ctx context.Context, e Event) error {
	return s.dispatch(ctx, e, nil)
}

func (s Service) dispatch(ctx context.Context, e Event, preparationErr error) error {

	hooks, err := s.List(ctx, e.Repo)
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range hooks {
		if h.Event == e.Event && !h.Disabled && h.OnFail != "block" && (e.Name == "" || e.Name == h.Name) {
			_, err := s.run(ctx, e, h.Name, preparationErr, false)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Tick uses persisted state to claim schedule intervals and origin/main changes
// once across all maintenance processes. Main hooks refresh origin/main at most
// once every five minutes, independently of tracked pull requests.
// The first main observation establishes a baseline without emitting an event.
func (s Service) Tick(ctx context.Context, repo string, now time.Time) error {
	hooks, err := s.List(ctx, repo)
	if err != nil || len(hooks) == 0 {
		return err
	}
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return err
	}
	fetchDue := false
	for _, h := range hooks {
		if h.Event == MainUpdated && !h.Disabled {
			if err := s.runtime(r.ID, func(st *state) error {
				if st.FetchedAt.IsZero() || !now.Before(st.FetchedAt.Add(5*time.Minute)) {
					st.FetchedAt = now
					fetchDue = true
				}
				return nil
			}); err != nil {
				return err
			}
			break
		}
	}
	var fetchErr error
	if fetchDue {
		fetch := s.Fetch
		if fetch == nil {
			fetch = func(ctx context.Context, repo string) error {
				_, err := repoid.Git(ctx, repo, "fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "origin", "+refs/heads/main:refs/remotes/origin/main")
				return err
			}
		}
		fetchErr = fetch(ctx, r.Root)
	}
	main, _ := repoid.Git(ctx, r.Root, "rev-parse", "--verify", "refs/remotes/origin/main^{commit}")
	var events []Event
	err = s.runtime(r.ID, func(st *state) error {
		if main != "" {
			if st.Main != "" && st.Main != main {
				events = append(events, Event{Event: MainUpdated, Repo: r.Root, Worktree: r.Main, Branch: "main", SHA: main})
			}
			st.Main = main
		}
		for _, h := range hooks {
			if h.Event != Schedule || h.Disabled || Validate(h) != nil {
				continue
			}
			key := scheduleKey(h)
			due := st.Due[key]
			last, known := st.Every[key]
			switch {
			case !due.IsZero() && known && last != h.EveryMinutes:
				// An edited interval moves the run that the old interval made
				// due: the last run plus the new interval.
				due = due.Add(time.Duration(h.EveryMinutes-last) * time.Minute)
			case !due.IsZero() && !known && due.Before(now.Add(time.Duration(h.EveryMinutes)*time.Minute)):
				// An older release wrote this run time without the interval that
				// set it, so it may predate an interval edit. Wait one full
				// interval from here rather than fire at the old deadline.
				due = now.Add(time.Duration(h.EveryMinutes) * time.Minute)
			}
			st.Due[key] = due
			st.Every[key] = h.EveryMinutes
			if now.Before(due) {
				continue
			}
			st.Due[key] = now.Add(time.Duration(h.EveryMinutes) * time.Minute)
			events = append(events, Event{Event: Schedule, Repo: r.Root, Worktree: r.Main, Name: h.Name})
		}
		return nil
	})
	if err != nil {
		return err
	}
	errs := []error{fetchErr}
	for _, e := range events {
		errs = append(errs, s.Emit(ctx, e))
	}
	return errors.Join(errs...)
}

func validCommitSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}

// snapshot prepares the immutable queued commit after the engine takes the event.
func snapshot(ctx context.Context, e Event) (Event, error) {
	if !validCommitSHA(e.SHA) {
		return e, fmt.Errorf("queued hook event has no full commit SHA: %q", e.SHA)
	}
	root, err := os.MkdirTemp("", "dyna-hook-*")
	if err != nil {
		return e, err
	}
	tree := filepath.Join(root, "tree")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err = repoid.Git(ctx, e.Repo, "worktree", "add", "--detach", tree, e.SHA); err != nil {
		_ = os.RemoveAll(root)
		return e, err
	}
	e.Worktree = tree
	e.Snapshot = true
	return e, nil
}
func cleanupSnapshot(e Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := repoid.Git(ctx, e.Repo, "worktree", "remove", "--force", e.Worktree)
	if err == nil {
		_ = os.Remove(filepath.Dir(e.Worktree))
	}
}
