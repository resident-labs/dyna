package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const quiet = `NOT EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NULL)
AND NOT EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=s.id AND wake=1 AND state='pending')
AND EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND shell_jobs=0 AND ended_at<=? AND id=(SELECT id FROM turns WHERE session=s.id ORDER BY started_at DESC, rowid DESC LIMIT 1))`
const waking = `EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=s.id AND wake=1 AND state='pending')`

// DormancyCandidates finds quiet boxes and boxes with pending wakes.
func (s *Store) DormancyCandidates(ctx context.Context, cutoff time.Time) ([]contract.SessionID, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT s.id FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.closed_at IS NULL AND ((b.state='ready' AND `+quiet+`) OR (b.state IN ('stopped','pending') AND b.host IS NOT NULL AND b.image != '' AND `+waking+`))`, cutoff.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []contract.SessionID
	for rows.Next() {
		var id contract.SessionID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) CanDispatch(ctx context.Context, id contract.SessionID) (bool, error) {
	var ok bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id=? AND s.closed_at IS NULL AND s.state!='dormant' AND b.state='ready')`, id).Scan(&ok)
	return ok, err
}

// StopIdle rechecks quietness while committing the stopped box.
func (s *Store) StopIdle(ctx context.Context, b box.Box, event contract.Event, cutoff time.Time) (bool, error) {
	return s.dormancyTransition(ctx, b, contract.BoxStopped, "", event, "dormant", quiet, cutoff)
}

// Wake commits admission or a retryable host failure with the session state.
func (s *Store) Wake(ctx context.Context, b box.Box, to contract.BoxState, reason string, event contract.Event) (bool, error) {
	state, condition := "dormant", waking
	if to == contract.BoxStarting {
		state = "open"
	} else if b.State == contract.BoxStarting {
		condition = "1"
	}
	return s.dormancyTransition(ctx, b, to, reason, event, state, condition, event.Time)
}

func (s *Store) dormancyTransition(ctx context.Context, b box.Box, to contract.BoxState, reason string, event contract.Event, state, condition string, cutoff time.Time) (bool, error) {
	next, body, err := box.Transition(b, to, reason, event.Time)
	if err != nil {
		return false, err
	}
	event.Body = body
	var changed bool
	err = s.Write(ctx, func(tx *sql.Tx) error {
		query := `UPDATE sessions AS s SET state=? WHERE id=? AND closed_at IS NULL AND EXISTS (SELECT 1 FROM boxes WHERE session=s.id AND state=?) AND (` + condition + `)`
		var result sql.Result
		var err error
		if condition == quiet {
			result, err = tx.ExecContext(ctx, query, state, b.Session, b.State, cutoff.UnixMilli())
		} else {
			result, err = tx.ExecContext(ctx, query, state, b.Session, b.State)
		}
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE boxes SET state=?,reason=?,updated_at=? WHERE id=? AND state=?", next.State, reason, event.Time.UnixMilli(), b.ID, b.State)
		if err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		changed = err == nil
		return err
	})
	return changed, err
}
