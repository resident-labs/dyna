package worktrees

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const (
	portAlertBandKey       = "worktrees.portAlertBand"
	portAlertGenerationKey = "worktrees.portAlertGeneration"
	portAlertAckGenKey     = "worktrees.portAlertAckGeneration"
	portAlertAckBandKey    = "worktrees.portAlertAckThreshold"
)

// PortCapacityAlert is one upward crossing of the worktree port pool's
// capacity bands. Generation identifies one continuous high-capacity period,
// and Threshold is the crossed percentage of the pool. ObservePortCapacity
// returns the same crossing until AckPortAlertDelivery confirms delivery, so
// a failed post never loses the alert.
type PortCapacityAlert struct {
	Used       int
	Limit      int
	Threshold  int
	Generation int
}

type portAlertState struct {
	used       int
	band       int
	generation int
	ackGen     int
	ackBand    int
}

type portAlertReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ObservePortCapacity reports a new 90% or 95% pool crossing, and keeps
// reporting an undelivered crossing until it is acknowledged. It records a
// high-water band in SQLite, so repeated allocations and engine restarts do
// not repeat an alert. Use below 90%, observed at allocation or release,
// rearms the next capacity period.
func ObservePortCapacity() (*PortCapacityAlert, error) {
	db, err := registryDB()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	current, err := readPortAlertState(ctx, db)
	if err != nil {
		return nil, err
	}
	alert, err := portAlertTransition(ctx, db, current)
	if err != nil || alert == nil {
		return nil, err
	}
	return alert, nil
}

// portAlertTransition computes one step of the alert state machine inside a
// write transaction, rereading the state there so concurrent observers agree.
func portAlertTransition(ctx context.Context, db *sql.DB, snapshot portAlertState) (*PortCapacityAlert, error) {
	var alert *PortCapacityAlert
	err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		current, err := readPortAlertState(ctx, conn)
		if err != nil {
			return err
		}
		alert, err = applyPortAlertState(ctx, conn, current)
		return err
	})
	if err != nil {
		return nil, err
	}
	return alert, nil
}

// applyPortAlertState writes one state step and returns the alert it produces,
// or nil when there is nothing new to deliver. A crossing persists the band
// and generation before any delivery, and keeps returning that crossing until
// AckPortAlertDelivery records its delivery.
func applyPortAlertState(ctx context.Context, conn *sql.Conn, current portAlertState) (*PortCapacityAlert, error) {
	band := portCapacityBand(current.used)
	if band == 0 {
		if current.band == 0 {
			return nil, nil
		}
		// Use fell below the first band: the next high-capacity period alerts again.
		return nil, writePortAlertMeta(ctx, conn, map[string]string{
			portAlertBandKey:    "0",
			portAlertAckGenKey:  "0",
			portAlertAckBandKey: "0",
		})
	}
	if current.ackGen == current.generation && band <= current.ackBand {
		// This crossing, or a higher one, was already delivered.
		return nil, nil
	}
	if band <= current.band {
		// A crossing of the current band was not delivered yet: report it again.
		return &PortCapacityAlert{Used: current.used, Limit: portSlots, Threshold: current.band, Generation: current.generation}, nil
	}
	if current.band == 0 {
		current.generation++
	}
	alert := &PortCapacityAlert{Used: current.used, Limit: portSlots, Threshold: band, Generation: current.generation}
	if err := writePortAlertMeta(ctx, conn, map[string]string{
		portAlertBandKey:       strconv.Itoa(band),
		portAlertGenerationKey: strconv.Itoa(current.generation),
	}); err != nil {
		return nil, err
	}
	return alert, nil
}

// AckPortAlertDelivery records that the alert of one generation and threshold
// was delivered to every room. The reporter acknowledges only after a full
// sweep succeeds, so a transient failure is retried on the next observation.
func AckPortAlertDelivery(generation, threshold int) error {
	db, err := registryDB()
	if err != nil {
		return err
	}
	ctx := context.Background()
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return writePortAlertMeta(ctx, conn, map[string]string{
			portAlertAckGenKey:  strconv.Itoa(generation),
			portAlertAckBandKey: strconv.Itoa(threshold),
		})
	})
}

// rearmPortCapacity runs inside a transaction that releases a port block.
// When the release drops pool use below the 90% band, the next high-capacity
// period can alert again without waiting for the next allocation to observe
// the recovery.
func rearmPortCapacity(ctx context.Context, conn *sql.Conn) error {
	current, err := readPortAlertState(ctx, conn)
	if err != nil {
		return err
	}
	if current.band == 0 || portCapacityBand(current.used) != 0 {
		return nil
	}
	return writePortAlertMeta(ctx, conn, map[string]string{
		portAlertBandKey:    "0",
		portAlertAckGenKey:  "0",
		portAlertAckBandKey: "0",
	})
}

func writePortAlertMeta(ctx context.Context, conn *sql.Conn, values map[string]string) error {
	for key, value := range values {
		if _, err := conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
			return err
		}
	}
	return nil
}

// portCapacityBand returns the highest alert threshold that used reaches.
func portCapacityBand(used int) int {
	switch {
	case used >= portSlots*19/20:
		return 95
	case used >= portSlots*9/10:
		return 90
	default:
		return 0
	}
}

func readPortAlertState(ctx context.Context, q portAlertReader) (portAlertState, error) {
	var state portAlertState
	var band, generation, ackGen, ackBand string
	err := q.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM worktree_records WHERE COALESCE(json_extract(body,'$.port'),0) <> 0), COALESCE((SELECT value FROM meta WHERE key=?),'0'), COALESCE((SELECT value FROM meta WHERE key=?),'0'), COALESCE((SELECT value FROM meta WHERE key=?),'0'), COALESCE((SELECT value FROM meta WHERE key=?),'0')", portAlertBandKey, portAlertGenerationKey, portAlertAckGenKey, portAlertAckBandKey).Scan(&state.used, &band, &generation, &ackGen, &ackBand)
	if err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port capacity: %w", err)
	}
	if state.band, err = strconv.Atoi(band); err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert band: %w", err)
	}
	if state.generation, err = strconv.Atoi(generation); err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert generation: %w", err)
	}
	if state.ackGen, err = strconv.Atoi(ackGen); err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert acknowledgement: %w", err)
	}
	if state.ackBand, err = strconv.Atoi(ackBand); err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert acknowledged threshold: %w", err)
	}
	return state, nil
}
