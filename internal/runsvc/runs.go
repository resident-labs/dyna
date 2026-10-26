// Package runsvc holds the engine calls that change existing runs. The CLI
// calls them over the engine's Unix socket instead of writing run state
// itself (requirement 1). Their bindings are Local: they delete or force run
// state, and no UI needs them.
package runsvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runprocs"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktreesvc"
)

// Pause pauses or resumes a run. Only a running run pauses.
func Pause(ctx context.Context, id string, paused bool) error {
	m, err := runForCaller(ctx, id)
	if err != nil {
		return err
	}
	if paused && m.Status != "running" {
		return fmt.Errorf("run %s is not running (status %s)", id, m.Status)
	}
	return runstore.SetPaused(id, paused)
}

// runExitGrace is how long a canceled run's process may take to finalize
// before its processes are killed.
var runExitGrace = 10 * time.Second

// Cancel stops a run only in the caller's room. A stopped run's workers are
// gone, so their process groups end and their managed shell jobs stop the way
// a closed session's do (D-165, D-185).
func Cancel(ctx context.Context, id string) error {
	m, err := runForCaller(ctx, id)
	if err != nil {
		return err
	}
	if err := runstore.Cancel(id); err != nil {
		return err
	}
	// The run process finalizes its own state on SIGTERM; give it time, then
	// end every group it or its workers started, so a worker that is paused,
	// detached or in a group of its own does not outlive the run.
	graceCtx, stopGrace := context.WithTimeout(ctx, runExitGrace)
	for runstore.Alive(m) && graceCtx.Err() == nil {
		time.Sleep(50 * time.Millisecond)
	}
	stopGrace()
	leader := 0 // a recorded pid is the run's group only while that process lives
	if runstore.Alive(m) {
		leader = m.Pid
	}
	if err := runprocs.Kill(id, leader); err != nil {
		return fmt.Errorf("run %s is canceled, but ending its processes failed: %w", id, err)
	}
	if err := shells.DefaultStore().StopRun(id); err != nil {
		return fmt.Errorf("run %s is canceled, but stopping its shell jobs failed: %w", id, err)
	}
	return nil
}

// callerRoom returns an empty filter only for the owner. Other callers need
// their own room, including when old run metadata has no room.
func callerRoom(ctx context.Context) (string, error) {
	a := authority.Of(ctx)
	switch a.Role {
	case authority.Owner:
		return "", nil
	case authority.Root, authority.Session, authority.Worker:
		if a.Room != "" {
			return a.Room, nil
		}
	}
	return "", errors.New("this caller needs its own room to change runs")
}

func runForCaller(ctx context.Context, id string) (runstore.Meta, error) {
	if _, err := callerRoom(ctx); err != nil {
		return runstore.Meta{}, err
	}
	m, err := runstore.ReadMeta(id)
	if err != nil {
		return runstore.Meta{}, err
	}
	if _, err := authority.ActorIn(ctx, m.RoomID); err != nil {
		return runstore.Meta{}, err
	}
	return m, nil
}

// Remove applies the worktree disposition to a run, then deletes the run. A
// failed disposition keeps the run and comes back in the result, with its
// warnings, rather than as the call's error.
func Remove(ctx context.Context, id string, disposition worktrees.Disposition) (worktreesvc.DispositionResult, error) {
	if _, err := runForCaller(ctx, id); err != nil {
		return worktreesvc.DispositionResult{}, err
	}
	var res worktreesvc.DispositionResult
	err := runstore.RemoveWith(id, func(m runstore.Meta) error { return dispose(ctx, m.ID, disposition, &res) })
	if err != nil && res.Err() == nil {
		return res, err
	}
	return res, nil
}

// Cleared is what Clear did: the runs it removed, each run's worktree
// result, and the runs it could not remove.
type Cleared struct {
	Removed   int                             `json:"removed"`
	Worktrees []worktreesvc.DispositionResult `json:"worktrees,omitempty"`
	Failures  []string                        `json:"failures,omitempty"`
}

// Clear removes finished live runs in the caller's room, applying the
// worktree disposition to each first. The owner can clear every room.
func Clear(ctx context.Context, disposition worktrees.Disposition) (Cleared, error) {
	var out Cleared
	roomID, err := callerRoom(ctx)
	if err != nil {
		return out, err
	}
	n, failures, err := runstore.ClearCompletedInRoom(roomID, func(m runstore.Meta) error {
		var res worktreesvc.DispositionResult
		err := dispose(ctx, m.ID, disposition, &res)
		out.Worktrees = append(out.Worktrees, res)
		return err
	})
	out.Removed = n
	for _, f := range failures {
		out.Failures = append(out.Failures, f.Error())
	}
	return out, err
}

func dispose(ctx context.Context, id string, disposition worktrees.Disposition, res *worktreesvc.DispositionResult) error {
	r, err := worktreesvc.ApplyRunDisposition(ctx, id, disposition)
	if err != nil {
		return fmt.Errorf("worktree disposition for run %s failed: %w", id, err)
	}
	*res = r
	return r.Err()
}

// Archived is one archived run.
type Archived struct {
	Dir string `json:"dir"`
}

// Archive moves one finished run into the archive.
func Archive(ctx context.Context, id string) (Archived, error) {
	if _, err := runForCaller(ctx, id); err != nil {
		return Archived{}, err
	}
	res, err := runstore.ArchiveRun(id)
	if err != nil {
		return Archived{}, err
	}
	return Archived{Dir: res.Dir}, nil
}

// Unarchive moves an archived run back into the live catalog and returns its
// directory.
func Unarchive(ctx context.Context, id string) (string, error) {
	if _, err := runForCaller(ctx, id); err != nil {
		return "", err
	}
	return runstore.UnarchiveRun(id)
}
