// Package worktreesvc runs worktree commands in the engine.
package worktreesvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/nodedeps"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/worktrees"
)

// Outcome carries operation failures with partial decisions. Transport and
// authority failures use the call error instead. The CLI must check Err.
type Outcome struct {
	Failure string `json:"failure,omitempty"`
}

func (o Outcome) Err() error {
	if o.Failure == "" {
		return nil
	}
	return errors.New(o.Failure)
}

func failure(err error) Outcome {
	if err == nil {
		return Outcome{}
	}
	return Outcome{Failure: err.Error()}
}

// caller refuses an absent or incomplete engine identity before any store opens.
func caller(ctx context.Context) (authority.Actor, error) {
	a := authority.Of(ctx)
	valid := false
	switch a.Role {
	case authority.Owner:
		valid = true
	case authority.Root, authority.Session:
		valid = a.Session != ""
	case authority.Worker:
		valid = a.Run != ""
	}
	if !valid {
		return authority.Actor{}, errors.New("the engine could not verify who is changing worktrees")
	}
	return a, nil
}

type NewResult struct {
	Record  worktrees.Record `json:"record"`
	Notices []string         `json:"notices,omitempty"`
}

func New(ctx context.Context, repo, branch, base string, noDeps bool, session string) (NewResult, error) {
	a, err := caller(ctx)
	if err != nil {
		return NewResult{}, err
	}
	rec, err := worktrees.NewFor(ctx, repo, branch, base, session, "")
	if err != nil {
		return NewResult{}, err
	}
	_ = hooks.Emit(ctx, hooks.Event{Event: hooks.WorktreeCreated, Repo: rec.Path, Worktree: rec.Path, Branch: branch, Session: session, Room: a.Room})
	result := NewResult{Record: rec}
	if !noDeps {
		// Dependency restoration is best effort, as it was in the CLI.
		summary, err := nodedeps.Prepare(ctx, rec.Path)
		if summary != "" {
			result.Notices = append(result.Notices, summary)
		}
		if err != nil {
			result.Notices = append(result.Notices, "node_modules: prepare failed: "+err.Error())
		}
	}
	return result, nil
}

type GCResult struct {
	Outcome
	Trees []worktrees.Tree `json:"trees"`
}

func GC(ctx context.Context, repo, session string, dryRun bool) (GCResult, error) {
	if _, err := caller(ctx); err != nil {
		return GCResult{}, err
	}
	trees, err := worktrees.GC(ctx, repo, worktrees.GCOptions{DryRun: dryRun, Session: session, OwnerClosed: worktrees.SessionClosed})
	return GCResult{Outcome: failure(err), Trees: trees}, nil
}

type RemoveResult struct {
	Outcome
	Lines []string `json:"lines"`
}

func Remove(ctx context.Context, names []string, force bool) (RemoveResult, error) {
	if _, err := caller(ctx); err != nil {
		return RemoveResult{}, err
	}
	var result RemoveResult
	var errs []error
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		rec, err := worktrees.Get(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		state, err := worktrees.Inspect(rec)
		if err != nil {
			if !force {
				errs = append(errs, err)
				result.Lines = append(result.Lines, fmt.Sprintf("retained %s (inspection failed)", rec.Path))
				continue
			}
			state = worktrees.StateModified
		}
		if state == worktrees.StateModified && !force {
			result.Lines = append(result.Lines, fmt.Sprintf("retained %s (modified since retention; use --force)", rec.Path))
			errs = append(errs, fmt.Errorf("worktree %s is modified since retention", rec.Path))
			continue
		}
		if err := worktrees.Remove(rec, force); err != nil {
			errs = append(errs, err)
			continue
		}
		result.Lines = append(result.Lines, "removed "+rec.Path)
	}
	result.Outcome = failure(errors.Join(errs...))
	return result, nil
}

type PruneDecision struct {
	Path   string `json:"path"`
	RunID  string `json:"runId"`
	State  string `json:"state"`
	Action string `json:"action"`
}

type PruneResult struct {
	Outcome
	Decisions []PruneDecision `json:"decisions"`
	Warnings  []string        `json:"warnings,omitempty"`
}

func Prune(ctx context.Context, age time.Duration, force, dryRun bool) (PruneResult, error) {
	if _, err := caller(ctx); err != nil {
		return PruneResult{}, err
	}
	if age < 0 {
		return PruneResult{}, errors.New("--older-than must be a non-negative duration")
	}
	records, warnings, err := worktrees.ListWithWarnings()
	result := PruneResult{Decisions: make([]PruneDecision, 0), Warnings: messages(warnings)}
	if err != nil {
		return result, err
	}
	now := time.Now()
	var errs []error
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		runMissing := false
		if rec.RunID != "" {
			_, statErr := os.Stat(runstore.RunDir(rec.RunID))
			runMissing = os.IsNotExist(statErr)
		}
		if now.Sub(rec.RetainedAt) < age && !runMissing {
			continue
		}
		state, err := worktrees.Inspect(rec)
		if err != nil {
			if !force {
				errs = append(errs, fmt.Errorf("inspect %s: %w", rec.Path, err))
				result.Decisions = append(result.Decisions, PruneDecision{Path: rec.Path, RunID: rec.RunID, State: "error", Action: worktrees.ActionSkipped})
				continue
			}
			state = worktrees.StateModified
		}
		action := worktrees.ActionRemoved
		if state == worktrees.StateModified && !force {
			action = worktrees.ActionSkipped
		}
		result.Decisions = append(result.Decisions, PruneDecision{Path: rec.Path, RunID: rec.RunID, State: state, Action: action})
		if dryRun || action == worktrees.ActionSkipped {
			continue
		}
		if err := worktrees.Remove(rec, force); err != nil {
			errs = append(errs, err)
			result.Decisions[len(result.Decisions)-1].Action = worktrees.ActionSkipped
		}
	}
	result.Outcome = failure(errors.Join(errs...))
	return result, nil
}

type DispositionResult struct {
	Outcome
	Actions  []worktrees.Action `json:"actions"`
	Warnings []string           `json:"warnings,omitempty"`
}

func ApplyRunDisposition(ctx context.Context, runID string, disposition worktrees.Disposition) (DispositionResult, error) {
	if _, err := caller(ctx); err != nil {
		return DispositionResult{}, err
	}
	actions, warnings, err := worktrees.ApplyRunDispositionWithWarnings(runID, disposition)
	return DispositionResult{Outcome: failure(err), Actions: actions, Warnings: messages(warnings)}, nil
}

func messages(errs []error) []string {
	var out []string
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}
