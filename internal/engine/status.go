package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
)

// StatusTimeout is how long `dyna engine status` waits for the Health call.
// An engine that does not answer by then is reported as not answering.
const StatusTimeout = 5 * time.Second

// Status prints the engine's Health for `dyna engine status`. ctx carries the
// deadline; call is engineclient's Call.
func Status(ctx context.Context, call func(context.Context, string, []any, any) error, out io.Writer, now time.Time) error {
	var h Health
	if err := call(ctx, "Health", nil, &h); err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("engine not answering: no Health reply in %s", StatusTimeout)
		case errors.Is(err, engineclient.ErrNotRunning):
			return errors.New("engine not running")
		}
		return fmt.Errorf("engine status: %w", err)
	}
	state := "ok"
	if h.Degraded {
		state = "degraded"
	}
	if h.Status != "" {
		state += " (" + h.Status + ")"
	}
	ms := func(d time.Duration) string { return d.Round(time.Millisecond).String() }
	w := h.Writes
	fmt.Fprintf(out, "engine: %s\n", state)
	if h.Connect != "" {
		fmt.Fprintf(out, "connect: %s\n", h.Connect)
	}
	fmt.Fprintf(out, "started: %s (%s ago)\n", h.Started.Format(time.DateTime), now.Sub(h.Started).Round(time.Second))
	fmt.Fprintf(out, "writes, last 5 min: %d, p50 %s, p99 %s, slowest %s", w.Writes, ms(w.P50), ms(w.P99), ms(w.Slowest))
	if w.SlowestKind != "" {
		fmt.Fprintf(out, " (%s)", w.SlowestKind)
	}
	fmt.Fprintf(out, "\nwrites in flight: %d, oldest %s; waiting for the writer lock: %d\n", w.InFlight, ms(w.Oldest), h.WritersWaiting)
	fmt.Fprintf(out, "events: revision %d, %d streams, largest lag %d, %d dropped\n", h.Events.Seq, h.Events.Streams, h.Events.MaxLag, h.Events.Dropped)
	fmt.Fprintf(out, "sqlite busy retries: %d\n", h.SQLiteBusyRetries)
	m := h.DatabaseMetrics
	fmt.Fprintf(out, "database duration counts since start (<=1ms, <=10ms, <=100ms, <=1s, >1s): hold %v, commit %v, checkpoint %v\n", m.WriteHold, m.Commit, m.CheckpointDuration)
	fmt.Fprintf(out, "scheduled checkpoints since start: total frames observed %d, copied %d, incomplete %d, errors %d\n", m.CheckpointFrames, m.CheckpointMoved, m.CheckpointIncomplete, m.CheckpointErrors)
	if len(m.Callers) > 0 {
		fmt.Fprintf(out, "top three writer callers by transactions: %s\n", topCallers(m.Callers, func(a, b database.CallerCount) int { return cmp.Compare(b.Transactions, a.Transactions) }))
		fmt.Fprintf(out, "top three writer callers by lock hold: %s\n", topCallers(m.Callers, func(a, b database.CallerCount) int { return cmp.Compare(b.Hold, a.Hold) }))
	}
	fmt.Fprintf(out, "callers refused: %d\n", h.CallersRefused)
	fmt.Fprintf(out, "safety sweep findings: %d\n", h.SweepFindings)
	return nil
}

// topCallers names up to three callers in the order that order sorts them, each
// with its transactions and lock hold time.
func topCallers(callers []database.CallerCount, order func(a, b database.CallerCount) int) string {
	sorted := slices.Clone(callers)
	slices.SortFunc(sorted, order)
	parts := make([]string, 0, 3)
	for _, c := range sorted[:min(3, len(sorted))] {
		parts = append(parts, fmt.Sprintf("%s %d transactions, held %s", c.Caller, c.Transactions, c.Hold.Round(time.Millisecond)))
	}
	return strings.Join(parts, "; ")
}

// healthStarting marks an engine that serves Health while migrations run.
const healthStarting = "starting"

// healthPoll is how often WaitHealthy asks again while the engine starts.
const healthPoll = 250 * time.Millisecond

// WaitHealthy waits, within ctx, until the engine answers Health. It fails
// when the engine never answers, or answers with a status. Health's status
// means "cannot take writes" (read-only, for example after a failed
// migration), so the install did not work (requirement 32). Informational
// notes go into their own Health fields, as Connect does. pause waits
// between tries; nil waits on a timer.
func WaitHealthy(ctx context.Context, call func(context.Context, string, []any, any) error, pause func(context.Context, time.Duration) error) error {
	if pause == nil {
		pause = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	answered := false
	for {
		callCtx, cancel := context.WithTimeout(ctx, StatusTimeout)
		var h Health
		err := call(callCtx, "Health", nil, &h)
		cancel()
		if err == nil {
			answered = true
			if h.Status == healthStarting && !h.Ready {
				// Migrations still run, but the listener is healthy. Keep waiting
				// for the full API instead of calling this slow start a failure.
			} else if h.Status != "" {
				return fmt.Errorf("the engine is up, but %s", h.Status)
			} else {
				return nil
			}
		}
		if pauseErr := pause(ctx, healthPoll); pauseErr != nil {
			if answered {
				return fmt.Errorf("the engine did not become ready: %w", pauseErr)
			}
			return fmt.Errorf("the engine did not answer Health: %w", err)
		}
	}
}
