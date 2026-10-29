package engine

import (
	"context"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/worktrees"
)

// Health is the answer of the engine's Health call. It shows an engine that
// hangs or falls behind at once: writes that do not end, a long queue for
// the writer lock, slow event streams, and SQLite lock contention.
type Health struct {
	// Status is "starting", "read-only mode", or empty for a normal engine.
	Status string `json:"status,omitempty"`
	// Ready reports that migrations finished and the full API can serve.
	Ready bool `json:"ready"`
	// Connect says why the engine did not join Resident Labs Connect, or is
	// empty. It is not Status: a writable engine still takes posts.
	Connect string       `json:"connect,omitempty"`
	Started time.Time    `json:"started"`
	Writes  state.Health `json:"writes"`
	// WritersWaiting counts writers waiting for the SQLite writer lock in
	// the engine, whether or not their store has moved to internal/state.
	WritersWaiting    int64            `json:"writersWaiting"`
	Events            hub.Stats        `json:"events"`
	SQLiteBusyRetries int64            `json:"sqliteBusyRetries"`
	DatabaseMetrics   database.Metrics `json:"databaseMetrics"`
	// CallersRefused counts socket callers the identity rules refused since
	// the engine started; while enforcement is off they were still served.
	CallersRefused int64 `json:"callersRefused"`
	// SweepFindings counts what the 10-minute safety sweep delivered since
	// the engine started: session turns, question answers and hook events.
	// Events should have delivered that input, so each one is a bug, and the
	// engine log names the sessions, rooms and spool files.
	SweepFindings int64 `json:"sweepFindings"`
	// WorktreePortBlocks counts registry records that hold a worktree port
	// block. It is nil while the engine starts or is read-only, and when the
	// registry cannot be read. It is raw: reclaimable blocks still count.
	WorktreePortBlocks *int `json:"worktreePortBlocks,omitempty"`
	Degraded           bool `json:"degraded"`
}

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
