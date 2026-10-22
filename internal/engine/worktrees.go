package engine

import (
	"context"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/worktreesvc"
)

func (s services) newWorktree(ctx context.Context, repo, branch, base string, noDeps bool) (worktreesvc.NewResult, error) {
	caller, local := websvc.CallerFromContext(ctx)
	creator, err := worktreeCreatedBy(caller, local, authority.Of(ctx), enforceCallers, log.Printf)
	if err != nil {
		return worktreesvc.NewResult{}, err
	}
	// A manually created worktree does not belong to its caller's workflow.
	// Run deletion must only remove worktrees created by workflow isolation.
	return worktreesvc.New(ctx, repo, branch, base, noDeps, creator)
}

func worktreeCreatedBy(caller websvc.Caller, local bool, actor authority.Actor, enforce bool, logf func(string, ...any)) (string, error) {
	as, ok, err := recordedCaller(caller, local, actor, enforce, logf, "creating a worktree")
	if err != nil {
		return "", err
	}
	if !ok {
		return unverified, nil
	}
	return as.Session, nil
}
