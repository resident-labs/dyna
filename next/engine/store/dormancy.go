package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const quiet = `NOT EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NULL)
AND (EXISTS (SELECT 1 FROM rooms WHERE id=s.room AND paused_at IS NOT NULL) OR NOT EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=s.id AND wake=1 AND state='pending'))
AND EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND shell_jobs=0 AND ended_at<=? AND id=(SELECT id FROM turns WHERE session=s.id ORDER BY started_at DESC, rowid DESC LIMIT 1))`

func (s *Store) CanDispatch(ctx context.Context, id contract.SessionID) (bool, error) {
	var ok bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id=? AND s.closed_at IS NULL AND s.state='open' AND b.want='run' AND b.state='ready')`, id).Scan(&ok)
	return ok, err
}

// StopQuiet requests stops for candidates that still satisfy the quiet policy.
func (s *Store) StopQuiet(ctx context.Context, ids []contract.SessionID, cutoff time.Time) ([]contract.SessionID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	var stopped []contract.SessionID
	err = s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `UPDATE boxes SET want='stop' WHERE session IN (SELECT value FROM json_each(?)) AND want='run' AND state='ready' AND EXISTS (SELECT 1 FROM sessions s WHERE s.id=boxes.session AND s.closed_at IS NULL AND s.state!='closing' AND `+quiet+`) RETURNING session`, string(data), cutoff.UnixMilli())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id contract.SessionID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			stopped = append(stopped, id)
		}
		return rows.Err()
	})
	return stopped, err
}

// dormantClose is shared by the candidate read and the final write check.
const dormantClose = `s.parent IS NOT NULL AND s.closed_at IS NULL AND s.state='open'
AND s.shell_jobs=0 AND r.paused_at IS NULL AND b.want='stop' AND b.state='stopped' AND b.updated_at<=?
AND NOT EXISTS (SELECT 1 FROM sessions c WHERE c.parent=s.id AND c.closed_at IS NULL)
AND NOT EXISTS (SELECT 1 FROM pr_owners p WHERE p.session=s.id)
AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=s.id AND d.state='pending')
AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.by_kind='session' AND o.by_id=s.id AND o.state='pending')
AND NOT EXISTS (SELECT 1 FROM turns t WHERE t.session=s.id AND t.ended_at IS NULL)`

func (s *Store) DormantCloseCandidates(ctx context.Context, cutoff time.Time) ([]contract.SessionID, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT s.id FROM sessions s JOIN rooms r ON r.id=s.room JOIN boxes b ON b.session=s.id WHERE `+dormantClose+` ORDER BY b.updated_at,s.id LIMIT 50`, cutoff.UnixMilli())
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

// MarkDormantClosing rechecks all evidence in the same write as the close intent.
func (s *Store) MarkDormantClosing(ctx context.Context, id contract.SessionID, cutoff time.Time) (bool, error) {
	var changed bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE sessions AS s SET state='closing',close_reason='dormant' WHERE s.id=? AND EXISTS (SELECT 1 FROM rooms r JOIN boxes b ON b.session=s.id WHERE r.id=s.room AND `+dormantClose+`)`, id, cutoff.UnixMilli())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		changed = true
		_, err = tx.ExecContext(ctx, "UPDATE boxes SET want='close',due_at=0 WHERE session=?", id)
		return err
	})
	return changed, err
}

// WakeTerminal reverses a quiet stop when a terminal opens.
func (s *Store) WakeTerminal(ctx context.Context, id contract.BoxID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boxes SET want='run' WHERE id=? AND want='stop'`, id)
		return err
	})
}
