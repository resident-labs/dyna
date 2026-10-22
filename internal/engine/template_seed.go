package engine

import (
	"context"
	"fmt"
	"path/filepath"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workflowtemplate"
)

func (s services) templateSeed(ctx context.Context, registry string) (workflowtemplate.SeedResult, error) {
	switch authority.Of(ctx).Role {
	case authority.Owner, authority.Root, authority.Session, authority.Worker, authority.Steward:
	default:
		return workflowtemplate.SeedResult{}, fmt.Errorf("template seed needs a verified caller")
	}
	if !filepath.IsAbs(registry) {
		return workflowtemplate.SeedResult{}, fmt.Errorf("template registry must be an absolute path")
	}
	return (workflowtemplate.Store{DataDir: store.DataDir()}).SeedShipped(ctx, workflowtemplate.New(registry))
}
