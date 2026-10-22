package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store"
)

func (s services) stampPlanAncestry(ctx context.Context, dir, parent string) (string, error) {
	actor := authority.Of(ctx)
	root := filepath.Join(store.DataDir(), "runs")
	rel, err := filepath.Rel(root, dir)
	if err != nil || !filepath.IsAbs(dir) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plan directory must be inside the engine run store")
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realRel, err := filepath.Rel(realRoot, realDir)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plan directory resolves outside the engine run store")
	}
	run := strings.Split(rel, string(filepath.Separator))[0]
	resolvedRun := strings.Split(realRel, string(filepath.Separator))[0]
	if resolvedRun != run {
		return "", fmt.Errorf("plan directory resolves to a different run")
	}
	if !strings.HasPrefix(run, "wf_") || len(run) == 3 {
		return "", fmt.Errorf("plan directory has no workflow run ID")
	}
	switch actor.Role {
	case authority.Owner, authority.Root, authority.Session:
	case authority.Worker:
		if actor.Run != run {
			return "", fmt.Errorf("workflow worker cannot change ancestry of %s", run)
		}
	default:
		return "", fmt.Errorf("the engine could not verify who is saving plan ancestry")
	}
	return (ancestry.Store{DataDir: store.DataDir()}).Stamp(ctx, dir, parent)
}
