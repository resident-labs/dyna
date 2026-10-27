package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"dyna-agent/internal/callspec"
)

// reconcileLostReason is why a run the engine found without a process failed.
const reconcileLostReason = "failed (the engine found no live process for this run at start)"

// RecordLaunchFailure publishes a run whose worker never started as failed, so
// its owner sees the failure and the run folder is no longer an unpublished
// orphan. The caller sets ID, Name, owner and room fields.
func RecordLaunchFailure(m Meta, cause error) error {
	m.Status, m.Error, m.Detached = "error", "worker did not start: "+cause.Error(), true
	m.StartedAt = time.Now()
	m.EndedAt = m.StartedAt
	if m.Build == "" {
		m.Build = callspec.Build
	}
	_, err := changeMeta(filepath.Join(RunsDir(), m.ID), m.ID, func(ctx context.Context, conn *sql.Conn) error { return upsertMeta(ctx, conn, m) })
	return err
}

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
