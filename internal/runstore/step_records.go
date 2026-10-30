package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// StepIntent records the planned effect before a command starts.
func StepIntent(ctx context.Context, runID, key string, ordinal, attempt int) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO workflow_step_records (run_id, call_key, ordinal, attempt, state) VALUES (?, ?, ?, ?, 'intent')`, runID, key, ordinal, attempt)
		return err
	})
}

// StepResult records the observed result of an attempted command.
func StepResult(ctx context.Context, runID, key string, ordinal, attempt int, externalID string, result any, runErr error) error {
	var errText string
	if runErr != nil {
		errText = runErr.Error()
	}
	payload, err := json.Marshal(struct {
		Result any    `json:"result"`
		Error  string `json:"error,omitempty"`
	}{Result: result, Error: errText})
	if err != nil {
		return err
	}
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		update, err := conn.ExecContext(ctx, `UPDATE workflow_step_records SET state='result', result=?, external_id=? WHERE run_id=? AND call_key=? AND ordinal=? AND attempt=? AND state='intent'`, string(payload), externalID, runID, key, ordinal, attempt)
		if err != nil {
			return err
		}
		n, err := update.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("step result: expected one intent, updated %d", n)
		}
		return nil
	})
}
