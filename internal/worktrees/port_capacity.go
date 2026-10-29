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
)

// PortCapacityAlert is one upward crossing of the worktree port pool's
// capacity bands. Generation changes after use falls below the first band, so
// a later high-capacity period alerts again without repeating within one period.
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
}

type portAlertReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ObservePortCapacity reports a new 90% or 95% pool crossing. It records a
// high-water mark in SQLite, so repeated allocations and engine restarts do
// not repeat an alert. Dropping below 90% rearms the next capacity period.
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
	band := portCapacityBand(current.used)
	if (band == 0 && current.band == 0) || (band > 0 && band <= current.band) {
		return nil, nil
	}
	var alert *PortCapacityAlert
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		current, err = readPortAlertState(ctx, conn)
		if err != nil {
			return err
		}
		band = portCapacityBand(current.used)
		if (band == 0 && current.band == 0) || (band > 0 && band <= current.band) {
			return nil
		}
		if band == 0 {
			_, err := conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", portAlertBandKey, "0")
			return err
		}
		if current.band == 0 {
			current.generation++
		}
		alert = &PortCapacityAlert{Used: current.used, Limit: portSlots, Threshold: band, Generation: current.generation}
		for key, value := range map[string]string{
			portAlertBandKey:       strconv.Itoa(band),
			portAlertGenerationKey: strconv.Itoa(current.generation),
		} {
			if _, err := conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
				return err
			}
		}
		return nil
	})
	return alert, err
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
	var band, generation string
	err := q.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM worktree_records WHERE COALESCE(json_extract(body,'$.port'),0) <> 0), COALESCE((SELECT value FROM meta WHERE key=?),'0'), COALESCE((SELECT value FROM meta WHERE key=?),'0')", portAlertBandKey, portAlertGenerationKey).Scan(&state.used, &band, &generation)
	if err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port capacity: %w", err)
	}
	state.band, err = strconv.Atoi(band)
	if err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert band: %w", err)
	}
	state.generation, err = strconv.Atoi(generation)
	if err != nil {
		return portAlertState{}, fmt.Errorf("read worktree port alert generation: %w", err)
	}
	return state, nil
}
