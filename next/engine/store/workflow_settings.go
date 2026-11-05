package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// ErrWorkflowRunLimit means a room already has as many runs going as its settings allow.
var ErrWorkflowRunLimit = errors.New("the room's workflow runs are at their limit")

// GlobalWorkflowScope is the room key of the global row of workflow_settings. No room has this ID.
const GlobalWorkflowScope contract.RoomID = ""

// WorkflowSettings reads every scope's own values, the global one under GlobalWorkflowScope. A scope with no row sets nothing.
func (s *Store) WorkflowSettings(ctx context.Context) (map[contract.RoomID]contract.WorkflowSettings, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT room,settings FROM workflow_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[contract.RoomID]contract.WorkflowSettings{}
	for rows.Next() {
		var room contract.RoomID
		var data string
		if err := rows.Scan(&room, &data); err != nil {
			return nil, err
		}
		var set contract.WorkflowSettings
		if err := json.Unmarshal([]byte(data), &set); err != nil {
			return nil, err
		}
		all[room] = set
	}
	return all, rows.Err()
}

// WorkflowEffective is what a room's workflow settings come to: the room's values over the global ones over the defaults.
func (s *Store) WorkflowEffective(ctx context.Context, room contract.RoomID) (contract.WorkflowEffective, error) {
	all, err := s.WorkflowSettings(ctx)
	if err != nil {
		return contract.WorkflowEffective{}, err
	}
	return all[room].Over(all[GlobalWorkflowScope].Over(contract.WorkflowDefaults)), nil
}

// ChangeWorkflowSettings replaces one scope's values with what change makes of the current ones, in one write.
func (s *Store) ChangeWorkflowSettings(ctx context.Context, scope contract.RoomID, change func(contract.WorkflowSettings) contract.WorkflowSettings) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var current contract.WorkflowSettings
		var data string
		err := tx.QueryRowContext(ctx, "SELECT settings FROM workflow_settings WHERE room=?", scope).Scan(&data)
		if err == nil {
			err = json.Unmarshal([]byte(data), &current)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		encoded, err := json.Marshal(change(current))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO workflow_settings(room,settings) VALUES(?,?) ON CONFLICT(room) DO UPDATE SET settings=excluded.settings", scope, string(encoded))
		return err
	})
}
