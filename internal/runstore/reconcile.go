package runstore

import (
	"errors"
	"fmt"
)

// reconcileLostReason is why a run the engine found without a process failed.
const reconcileLostReason = "failed (the engine found no live process for this run at start)"

// ReconcileLostRuns marks the running runs whose process is gone as failed.
// A run outlives the engine, so a run left behind by a crash or a reboot would
// otherwise report "running" for ever. The engine calls this at start, before
// it serves run reads.
func ReconcileLostRuns() (int, error) {
	runs, err := Running()
	if err != nil {
		return 0, err
	}
	n := 0
	var failures []error
	for _, m := range runs {
		if Alive(m) {
			continue
		}
		if err := ForceStatus(m.ID, "error", reconcileLostReason); err != nil {
			failures = append(failures, fmt.Errorf("mark run %s failed: %w", m.ID, err))
			continue
		}
		n++
	}
	return n, errors.Join(failures...)
}
