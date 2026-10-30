package engine

import (
	"context"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/worktrees"
)

// maxWaiting is the writer-lock queue above which the engine is degraded.
const maxWaiting = 10

// engineHealth builds the Health call. writer is nil in a read-only engine.
// The port count runs only in a normal engine, so Health never waits on the
// registry migrations that run during startup.
func engineHealth(status, connect string, started time.Time, writer *state.Writer, events *hub.Hub, callers *socketCallers, signals *workerSignals) func() Health {
	return func() Health {
		h := Health{Status: status, Ready: true, Connect: connect, Started: started, Writes: writer.Health(), WritersWaiting: database.Waiting(), Events: events.Stats(), SQLiteBusyRetries: database.BusyRetries(), DatabaseMetrics: database.Measurements(), CallersRefused: callers.Refused(), SweepFindings: signals.findings()}
		h.Degraded = h.Writes.Degraded || h.WritersWaiting > maxWaiting
		if status == "" {
			if blocks, err := worktrees.AllocatedPortBlocks(context.Background()); err == nil {
				h.WorktreePortBlocks = &blocks
			}
		}
		return h
	}
}
