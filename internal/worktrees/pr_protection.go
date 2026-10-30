package worktrees

import (
	"context"
	"fmt"

	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/store"
)

// trackedPRProtection fails closed when the PR state cannot be read.
func trackedPRProtection(ctx context.Context, path string) (bool, error) {
	protected, err := prstate.OpenTrackedCheckout(ctx, prstate.Root(store.DataDir()), path)
	if err != nil {
		return false, fmt.Errorf("read tracked PR checkouts: %w", err)
	}
	return protected, nil
}
