package engine

import (
	"context"
	"time"

	"dyna-agent/internal/hooks"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/store"
	"dyna-agent/internal/updaterservice"
)

func scheduledUpdate(ctx context.Context, h hooks.Service, now time.Time) error {
	cfg, due, err := (updaterservice.Store{DataDir: store.DataDir()}).ClaimInterval(ctx, now)
	if err != nil || !due {
		return err
	}
	r, err := repoid.Resolve(ctx, cfg.Source)
	if err != nil {
		return err
	}
	branch, err := repoid.DefaultBranch(ctx, r.Root, repoid.Git)
	if err != nil {
		return err
	}
	sha, err := repoid.Git(ctx, r.Root, "rev-parse", "--verify", "origin/"+branch.Name+"^{commit}")
	if err != nil {
		return err
	}
	_, err = h.StartUpdate(ctx, hooks.Event{Event: hooks.Schedule, Repo: r.Root, Worktree: r.Main, Branch: branch.Name, SHA: sha}, cfg.Source)
	return err
}
