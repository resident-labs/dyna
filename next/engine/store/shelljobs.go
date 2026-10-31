package store

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract"
)

// ShellJobEnded appends the event and a waking delivery to the session itself.
// A job that shell_jobs already holds is skipped, so a retry adds no second wake.
func (s *Store) ShellJobEnded(ctx context.Context, session contract.SessionID, job contract.ShellJobID, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO shell_jobs(session,job) VALUES(?,?)", session, job)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		// While a turn is open its end replaces the count, so only an idle session lowers it here.
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET shell_jobs=MAX(0,shell_jobs-1) WHERE id=? AND NOT EXISTS (SELECT 1 FROM turns WHERE session=? AND ended_at IS NULL)", session, session); err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, contract.Delivery{To: event.From, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
	})
}
