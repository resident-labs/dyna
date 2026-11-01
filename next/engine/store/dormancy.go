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

// closable holds every rule an idle leaf must pass before the engine closes it unasked.
const closable = `s.parent IS NOT NULL AND s.closed_at IS NULL AND s.state='open'
AND s.shell_jobs=0 AND r.paused_at IS NULL AND (b.state='failed' OR (b.want='stop' AND b.state='stopped'))
AND NOT EXISTS (SELECT 1 FROM sessions c WHERE c.parent=s.id AND c.closed_at IS NULL)
AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=s.id AND d.state='pending')
AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.by_kind='session' AND o.by_id=s.id AND o.state IN ('pending','approved','running'))
AND ` + noOpenQuestion + `
AND NOT EXISTS (SELECT 1 FROM turns t WHERE t.session=s.id AND t.ended_at IS NULL)`

// aged is the normal wait. A host at its floor skips it, and the PR and goal exemptions with it: close archives the work and hands the goals on.
const aged = `b.updated_at<=? AND NOT EXISTS (SELECT 1 FROM pr_owners p WHERE p.session=s.id) AND NOT EXISTS (SELECT 1 FROM goals g WHERE g.session=s.id AND g.state='active')`

// atFloor needs a run of samples, so one bad reading closes nothing. Its parameter is the latest time the run may have started.
const atFloor = `h.state='ready' AND h.disk_bytes<=h.disk_floor AND h.disk_low_since BETWEEN 1 AND ?`

// closeRoom bounds what eviction may have open on a host. A close that cannot finish stays running and holds its box, so a failure
// common to every box, such as a remote outage, must stop eviction rather than start the rest of the host without admission.
const closeRoom = `(SELECT count(*) FROM boxes c WHERE c.host=b.host AND c.want IN ('close','discard') AND c.state!='deleted' AND c.reason='')<4
AND (SELECT count(*) FROM boxes c WHERE c.host=b.host AND c.want IN ('close','discard') AND c.state!='deleted' AND c.reason!='')<8`

const hostAtFloor = `EXISTS (SELECT 1 FROM hosts h WHERE h.name=b.host AND ` + atFloor + `)`

// dormantClose is the final write check, for both the sweep and disk eviction.
const dormantClose = closable + ` AND ((` + aged + `) OR ` + hostAtFloor + `)`

func (s *Store) DormantCloseCandidates(ctx context.Context, cutoff time.Time) ([]contract.SessionID, error) {
	return s.closeCandidates(ctx, `SELECT s.id FROM sessions s JOIN rooms r ON r.id=s.room JOIN boxes b ON b.session=s.id WHERE `+closable+` AND `+aged+` ORDER BY b.updated_at,s.id LIMIT 200`, cutoff.UnixMilli())
}

// DiskEvictCandidates lists the oldest idle leaves on hosts that have stayed at their floor since lowSince.
// Reading hosts first keeps it cheap when none is low. It takes four at a time, since each close starts its box to archive.
func (s *Store) DiskEvictCandidates(ctx context.Context, lowSince time.Time) ([]contract.SessionID, error) {
	return s.closeCandidates(ctx, `SELECT s.id FROM hosts h CROSS JOIN boxes b ON b.host=h.name JOIN sessions s ON s.id=b.session JOIN rooms r ON r.id=s.room WHERE `+atFloor+` AND `+closable+` AND `+closeRoom+` ORDER BY b.updated_at,s.id LIMIT 4`, lowSince.UnixMilli())
}

func (s *Store) closeCandidates(ctx context.Context, query string, args ...any) ([]contract.SessionID, error) {
	rows, err := s.Read().QueryContext(ctx, query, args...)
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
func (s *Store) MarkDormantClosing(ctx context.Context, id contract.SessionID, cutoff, lowSince time.Time) (bool, error) {
	var changed bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE sessions AS s SET state='closing',close_reason=(SELECT CASE WHEN `+hostAtFloor+` THEN 'disk' ELSE 'dormant' END FROM boxes b WHERE b.session=s.id) WHERE s.id=? AND EXISTS (SELECT 1 FROM rooms r JOIN boxes b ON b.session=s.id WHERE r.id=s.room AND `+dormantClose+`)`, lowSince.UnixMilli(), id, cutoff.UnixMilli(), lowSince.UnixMilli())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		changed = true
		if _, err := tx.ExecContext(ctx, `UPDATE hostexec_runs SET stopped='closed' WHERE ended_at IS NULL AND grant_id IN (SELECT id FROM grants WHERE to_kind='session' AND to_id=?)`, id); err != nil {
			return err
		}
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
