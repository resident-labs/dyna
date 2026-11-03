package store

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract"
)

// ShellJobEnded takes the job off what the session holds, and appends the event and a waking delivery to the session itself
// unless the end is silent. A job that shell_jobs already holds is skipped, so a retry adds no second wake.
func (s *Store) ShellJobEnded(ctx context.Context, session contract.SessionID, job contract.ShellJobID, event contract.Event, silent bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO shell_jobs(session,job) VALUES(?,?)", session, job)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		if err := dropJobRunning(ctx, tx, session, job); err != nil {
			return err
		}
		// While a turn is open its end replaces the count, so only an idle session lowers it here.
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET shell_jobs=MAX(0,shell_jobs-1) WHERE id=? AND NOT EXISTS (SELECT 1 FROM turns WHERE session=? AND ended_at IS NULL)", session, session); err != nil {
			return err
		}
		if silent {
			return nil
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: event.From, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
	})
}

// jobRunningNotice finds a job's running status among deliveries. It is not work: it holds no close and does not pass to the
// parent, whose job it is not.
const jobRunningNotice = `EXISTS (SELECT 1 FROM events e WHERE e.seq=d.seq AND e.type='shell.job.running')`

// ShellJobRunning delivers a job's status. An idle session wakes for it, and a session with a turn open reads it with its next
// turn. The check is in this write, so a turn cannot open between it and the delivery. A job that has ended sends nothing.
func (s *Store) ShellJobRunning(ctx context.Context, session contract.SessionID, job contract.ShellJobID, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var ended, inTurn bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shell_jobs WHERE session=?1 AND job=?2),EXISTS (SELECT 1 FROM turns WHERE session=?1 AND ended_at IS NULL)`, session, job).Scan(&ended, &inTurn)
		if err != nil || ended {
			return err
		}
		if err := dropJobRunning(ctx, tx, session, job); err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: event.From, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: !inTurn})
	})
}

// dropJobRunning takes back a status nobody has read. A newer one replaces it, and once the job ends it is no longer true.
func dropJobRunning(ctx context.Context, tx *sql.Tx, session contract.SessionID, job contract.ShellJobID) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM deliveries AS d WHERE d.to_kind='session' AND d.to_id=? AND d.state='pending' AND EXISTS (SELECT 1 FROM events e WHERE e.seq=d.seq AND e.type='shell.job.running' AND json_extract(CAST(e.body AS TEXT),'$.job')=?)`, session, job)
	return err
}
