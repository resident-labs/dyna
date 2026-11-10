package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Engine process state is a singleton in meta; adding its key needs no schema migration.
const engineStateKey = "engine_state"

type engineState struct {
	Build     string `json:"build"`
	StartedAt int64  `json:"started_at"`
	StoppedAt *int64 `json:"stopped_at"`
}

// RecordEngineStart replaces the previous process state and notifies open roots atomically.
// A store's first engine process has no restart to report.
func (s *Store) RecordEngineStart(ctx context.Context, build string, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var value string
		err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", engineStateKey).Scan(&value)
		previous := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var state engineState
		if previous {
			if err := json.Unmarshal([]byte(value), &state); err != nil {
				return err
			}
		}
		current, err := json.Marshal(engineState{Build: build, StartedAt: now.UnixMilli()})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", engineStateKey, string(current)); err != nil {
			return err
		}
		if !previous {
			return nil
		}
		event := contract.EngineRestarted{Build: build, PreviousBuild: state.Build, Clean: state.StoppedAt != nil}
		if event.Clean {
			event.DownMillis = now.UnixMilli() - *state.StoppedAt
		}
		// A restart needs no action, so it waits for the root's next input and does not start a turn.
		return s.alertRoots(ctx, tx, event, now, false, "")
	})
}

// RecordEngineStop marks the running process clean before the store closes.
func (s *Store) RecordEngineStop(ctx context.Context, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE meta SET value=json_set(value,'$.stopped_at',?) WHERE key=?", now.UnixMilli(), engineStateKey)
		return err
	})
}
