package librarian

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dyna-agent/internal/hooks"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

// Interval is the shortest time between two librarian turns for one
// repository. Merges inside it wait for the next wake.
const Interval = 30 * time.Minute

// SmellInterval limits code-reading passes to four per repository per day.
const SmellInterval = 6 * time.Hour

// Service runs librarian wakes. Tests replace Sessions, Settings and Now.
type Service struct {
	// Hook functions are supplied by the engine's existing hook handlers.
	ListHooks  func(context.Context, string) ([]hooks.Hook, error)
	AddHook    func(context.Context, string, hooks.Hook) error
	RemoveHook func(context.Context, string, string) error
	DataDir    string
	Sessions   Sessions
	Issues     Issues
	Settings   func() (settings.Values, error)
	Now        func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Service) settings() (settings.Values, error) {
	if s.Settings != nil {
		return s.Settings()
	}
	return settings.Load(settings.DefaultPath())
}

// Result says what one wake did and why.
type Result struct {
	Action string `json:"action"` // off, current, batched, skipped, busy, assigned or started
	Reason string `json:"reason,omitempty"`
	Head   string `json:"head,omitempty"`
}

// Wake runs the cheap checks in order and gives the librarian a turn only
// when all pass: enabled, last turn finished, main moved, interval elapsed,
// gate, session idle. The whole wake holds the repository's librarian lock.
func (s Service) Wake(ctx context.Context, repoDir string) (Result, error) {
	mapped, err := s.wakeMap(ctx, repoDir)
	if err != nil || mapped.Action == "off" {
		return mapped, err
	}
	smelled, err := s.wakeSmells(ctx, repoDir)
	if err != nil {
		return Result{}, err
	}
	if mapped.Action == "current" && (smelled.Action == "assigned" || smelled.Action == "started" || smelled.Action == "busy") {
		return smelled, nil
	}
	return mapped, nil
}

func (s Service) wakeMap(ctx context.Context, repoDir string) (Result, error) {
	r, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return Result{}, err
	}
	v, err := s.settings()
	if err != nil {
		return Result{}, err
	}
	states := librarianstate.Store{DataDir: s.DataDir}
	st, err := states.Get(r.ID)
	if !v.LibraryOn(r.Main) || errors.Is(err, os.ErrNotExist) {
		return Result{Action: "off", Reason: "the librarian is off for " + r.Main + "; run dyna library enable"}, nil
	}
	if err != nil {
		return Result{}, err
	}
	lock, err := rolesession.Lock(filepath.Join(s.DataDir, "library"), subagent.RoleLibrarian, st.RoomID)
	if err != nil {
		return Result{}, fmt.Errorf("another librarian wake holds the lock: %w", err)
	}
	defer rolesession.Unlock(lock)
	if st, err = states.Get(r.ID); err != nil {
		return Result{}, err
	}

	// The assigned commit becomes the checkpoint only when its turn
	// succeeds. A failed, stopped or lost turn is retried from the old one.
	if st.Pending != "" {
		turn, err := s.Sessions.Turn(st.Session, st.PendingTurn)
		if err == nil && !turn.Done() {
			return Result{Action: "busy", Reason: "the librarian is still on its last turn", Head: st.Head}, nil
		}
		ok := err == nil && turn.Status == subagent.TurnOK
		if st, err = states.Update(r.ID, false, func(st *librarianstate.State) error {
			if ok {
				st.Head = st.Pending
			} else if turn.Status == subagent.TurnError && rolesession.ProviderFailure(turn.Error) {
				info, _, _ := s.Sessions.Inspect(st.Session)
				st.FailedProfiles = rolesession.RecordFailure(st.FailedProfiles, info, turn, s.now())
			}
			st.Pending, st.PendingTurn = "", 0
			return nil
		}); err != nil {
			return Result{}, err
		}
	}

	target, err := MainHead(ctx, r.Main)
	if err != nil {
		return Result{}, err
	}
	if target == st.Head {
		return Result{Action: "current", Head: target}, nil
	}
	if next := st.LastRun.Add(Interval); s.now().Before(next) {
		return Result{Action: "batched", Reason: "next run after " + next.Format(time.RFC3339), Head: st.Head}, nil
	}

	entries, err := library.Store{DataDir: s.DataDir}.List(r.ID, false)
	if err != nil {
		return Result{}, err
	}
	reason, err := Gate(ctx, r.Main, st.Head, target, entries)
	if err != nil {
		return Result{}, err
	}
	if reason == "" {
		_, err = states.Update(r.ID, false, func(st *librarianstate.State) error { st.Head = target; return nil })
		return Result{Action: "skipped", Reason: "no owned file changed beyond whitespace and no source file is unmapped", Head: target}, err
	}

	lookup := rolesession.Lookup(st.Session, st.RoomID, subagent.RoleLibrarian, s.Sessions.Inspect, func() (subagent.Info, error) { return s.Sessions.Named(st.RoomID, false) })
	info, turn, usable := lookup.Info, lookup.Turn, lookup.Usable
	if usable && rolesession.Busy(info) {
		return Result{Action: "busy", Reason: "the librarian session is busy", Head: st.Head}, nil
	}
	profiles, err := s.Sessions.Profiles()
	if err != nil {
		return Result{}, err
	}
	recent, err := s.Sessions.Failures(s.now())
	if err != nil {
		return Result{}, err
	}
	failed := rolesession.MergeFailures(st.FailedProfiles, recent)
	name, _, err := rolesession.ChooseProfile(profiles, v.LibraryProfile, failed, s.now(), subagent.RoleLibrarian)
	if err != nil {
		return Result{}, err
	}

	tree := TreePath(s.DataDir, r.ID)
	if err := syncTree(ctx, r.Main, tree, target); err != nil {
		return Result{}, err
	}
	task := Task(r.Main, tree, st.Head, target, reason, v.LibraryReadmeMaxLines)
	action, n := "assigned", 0
	if usable && info.Profile == name && !rolesession.NeedsRecreation(info, turn) {
		n, err = s.Sessions.Assign(ctx, info.ID, task)
	} else {
		if usable {
			if err := s.Sessions.Close(ctx, info.ID); err != nil {
				return Result{}, err
			}
		}
		action = "started"
		info, n, err = s.Sessions.Start(st.RoomID, name, tree, Goal(r.Main), task, false)
	}
	if err != nil {
		return Result{}, err
	}
	_, err = states.Update(r.ID, false, func(st *librarianstate.State) error {
		st.Session, st.Pending, st.PendingTurn, st.LastRun, st.FailedProfiles = info.ID, target, n, s.now(), failed
		return nil
	})
	return Result{Action: action, Reason: reason, Head: target}, err
}

// wakeSmells runs the independent code-reading pass. Its checkout, session,
// checkpoint and profile failures are separate from the feature map pass.
func (s Service) wakeSmells(ctx context.Context, repoDir string) (Result, error) {
	r, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return Result{}, err
	}
	v, err := s.settings()
	if err != nil {
		return Result{}, err
	}
	states := librarianstate.Store{DataDir: s.DataDir}
	st, err := states.Get(r.ID)
	if !v.LibraryOn(r.Main) || errors.Is(err, os.ErrNotExist) {
		return Result{Action: "off"}, nil
	}
	if err != nil {
		return Result{}, err
	}
	lock, err := rolesession.Lock(filepath.Join(s.DataDir, "library"), "librarian-smells", st.RoomID)
	if err != nil {
		return Result{}, fmt.Errorf("another librarian smell wake holds the lock: %w", err)
	}
	defer rolesession.Unlock(lock)
	if st, err = states.Get(r.ID); err != nil {
		return Result{}, err
	}
	if st.SmellPending != "" {
		turn, turnErr := s.Sessions.Turn(st.SmellSession, st.SmellPendingTurn)
		if turnErr == nil && !turn.Done() {
			return Result{Action: "busy", Reason: "the librarian smell pass is still on its last turn", Head: st.SmellHead}, nil
		}
		ok := turnErr == nil && turn.Status == subagent.TurnOK
		if st, err = states.Update(r.ID, false, func(st *librarianstate.State) error {
			if ok {
				st.SmellHead = st.SmellPending
			} else if turn.Status == subagent.TurnError && rolesession.ProviderFailure(turn.Error) {
				info, _, _ := s.Sessions.Inspect(st.SmellSession)
				st.SmellFailedProfiles = rolesession.RecordFailure(st.SmellFailedProfiles, info, turn, s.now())
			}
			st.SmellPending, st.SmellPendingTurn = "", 0
			return nil
		}); err != nil {
			return Result{}, err
		}
	}
	target, err := MainHead(ctx, r.Main)
	if err != nil {
		return Result{}, err
	}
	if st.SmellHead == "" {
		_, err = states.Update(r.ID, false, func(st *librarianstate.State) error { st.SmellHead = target; return nil })
		return Result{Action: "current", Reason: "smell pass baseline recorded", Head: target}, err
	}
	if target == st.SmellHead {
		return Result{Action: "current", Head: target}, nil
	}
	if next := st.SmellLastRun.Add(SmellInterval); s.now().Before(next) {
		return Result{Action: "batched", Reason: "next smell run after " + next.Format(time.RFC3339), Head: st.SmellHead}, nil
	}
	lookup := rolesession.Lookup(st.SmellSession, st.RoomID, subagent.RoleLibrarian, s.Sessions.Inspect, func() (subagent.Info, error) { return s.Sessions.Named(st.RoomID, true) })
	info, turn, usable := lookup.Info, lookup.Turn, lookup.Usable
	if usable && rolesession.Busy(info) {
		return Result{Action: "busy", Reason: "the librarian smell session is busy", Head: st.SmellHead}, nil
	}
	profiles, err := s.Sessions.Profiles()
	if err != nil {
		return Result{}, err
	}
	recent, err := s.Sessions.Failures(s.now())
	if err != nil {
		return Result{}, err
	}
	failed := rolesession.MergeFailures(st.SmellFailedProfiles, recent)
	name, _, err := rolesession.ChooseProfile(profiles, v.LibrarySmellProfile, failed, s.now(), subagent.RoleReviewer)
	if err != nil {
		return Result{}, err
	}
	tree := SmellTreePath(s.DataDir, r.ID)
	if err := syncTree(ctx, r.Main, tree, target); err != nil {
		return Result{}, err
	}
	task := SmellTask(r.Main, tree, st.SmellHead, target)
	action, n := "assigned", 0
	if usable && info.Profile == name && !rolesession.NeedsRecreation(info, turn) {
		n, err = s.Sessions.Assign(ctx, info.ID, task)
	} else {
		if usable {
			if err := s.Sessions.Close(ctx, info.ID); err != nil {
				return Result{}, err
			}
		}
		action = "started"
		info, n, err = s.Sessions.Start(st.RoomID, name, tree, SmellGoal(r.Main), task, true)
	}
	if err != nil {
		return Result{}, err
	}
	_, err = states.Update(r.ID, false, func(st *librarianstate.State) error {
		st.SmellSession, st.SmellPending, st.SmellPendingTurn, st.SmellLastRun, st.SmellFailedProfiles = info.ID, target, n, s.now(), failed
		return nil
	})
	return Result{Action: action, Reason: "code-reading smell pass", Head: target}, err
}

// MainHead returns the commit of the remote default branch, or of the local
// HEAD for a repository without a resolvable default branch, such as a
// detached checkout that has no origin/HEAD.
func MainHead(ctx context.Context, main string) (string, error) {
	revs := []string{"HEAD"}
	if branch, err := repoid.DefaultBranch(ctx, main, repoid.Git); err == nil {
		// The remote-tracking ref wins, because the local branch of the same
		// name can lag behind the remote default branch.
		for _, ref := range []string{branch.Ref, "origin/" + branch.Name} {
			if !slices.Contains(revs, ref) {
				revs = append([]string{ref}, revs...)
			}
		}
	}
	for _, rev := range revs {
		if sha, err := repoid.Git(ctx, main, "rev-parse", "-q", "--verify", rev+"^{commit}"); err == nil {
			return sha, nil
		}
	}
	return "", fmt.Errorf("cannot find the main commit of %s", main)
}

// TreePath is the librarian's detached checkout. Dyna owns it and resets it
// before each turn; the librarian edits docs only in its own worktrees.
func TreePath(dataDir, repo string) string {
	return filepath.Join(dataDir, "library", fmt.Sprintf("%x", sha256.Sum256([]byte(repo)))[:16], "tree")
}

// SmellTreePath is the smell pass's checkout, independent of the map checkout.
func SmellTreePath(dataDir, repo string) string {
	return filepath.Join(dataDir, "library", fmt.Sprintf("%x", sha256.Sum256([]byte(repo)))[:16], "smell-tree")
}

func syncTree(ctx context.Context, main, tree, target string) error {
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(tree), 0o700); err != nil {
			return err
		}
		_, _ = repoid.Git(ctx, main, "worktree", "prune")
		_, err := repoid.Git(ctx, main, "worktree", "add", "--detach", "--force", tree, target)
		return err
	}
	if _, err := repoid.Git(ctx, tree, "checkout", "-q", "--detach", "--force", target); err != nil {
		return err
	}
	_, err := repoid.Git(ctx, tree, "clean", "-fdq")
	return err
}
