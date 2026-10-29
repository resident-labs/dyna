package engine

import (
	"context"

	"dyna-agent/internal/localci"
)

// abandonLocalCIRun tears down one stale local CI run by its ID. The engine
// checks the ID's form here, once, and passes the heads of the runs in flight,
// so a live run of this engine is refused.
func (s services) abandonLocalCIRun(ctx context.Context, runID string) (localci.AbandonedRun, error) {
	id, err := localci.ParseRunID(runID)
	if err != nil {
		return localci.AbandonedRun{}, err
	}
	var inFlight []string
	if s.rooms != nil && s.rooms.PRs != nil {
		for _, run := range s.rooms.PRs.RunningCI() {
			inFlight = append(inFlight, run.Head)
		}
	}
	return localci.Store{}.AbandonRun(ctx, id, inFlight)
}
