package engine

import (
	"context"

	"dyna-agent/internal/localci"
)

// abandonLocalCIRun tears down one stale local CI run by its ID. The engine
// checks the ID's form here, once, before the teardown.
func (s services) abandonLocalCIRun(ctx context.Context, runID string) (localci.AbandonedRun, error) {
	id, err := localci.ParseRunID(runID)
	if err != nil {
		return localci.AbandonedRun{}, err
	}
	return localci.Store{}.AbandonRun(ctx, id)
}
