package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// agyAccount is the one Antigravity account every neon session uses. A second account would make it a column of the box; until then it is
// a name, and the table already holds a row per account.
const agyAccount = "main"

// limitAgy records that the account's usage limit resets after seconds, the reset an agy turn's failure stated, and keeps the later of two
// resets. It tells every room's root once per limit, in the owner's conversation with it and without waking it: a limit that was
// already in force is not told again, whatever the turns still ending on it say, since each of them states its own reset a second or
// two apart. The root reads the notice with its next input, so the owner hears of it once per room, whatever the number of sessions
// the limit holds.
func (s *Store) limitAgy(ctx context.Context, tx *sql.Tx, event contract.Event, seconds int64) error {
	until := event.Time.Add(time.Duration(seconds) * time.Second)
	var previous int64
	err := tx.QueryRowContext(ctx, "SELECT until FROM agy_limits WHERE account=?", agyAccount).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO agy_limits(account,until) VALUES(?1,?2) ON CONFLICT(account) DO UPDATE SET until=MAX(until,?2)", agyAccount, until.UnixMilli()); err != nil {
		return err
	}
	if previous > event.Time.UnixMilli() {
		return nil
	}
	return s.alertRoots(ctx, tx, func(root Session) contract.EventBody {
		return contract.SessionNotice{Session: root.ID, Text: fmt.Sprintf("Antigravity quota reached; neon sessions resume at %s UTC.", until.UTC().Format("15:04")), ClientID: fmt.Sprintf("agy-limit:%d", until.UnixMilli())}
	}, event.Time, false, "")
}

// agyLimited says the account's usage limit stands at now: asking Google again before the reset only fails again. It is the one check
// that decides whether an agy turn or compaction may go out, and the only state of the gate is its row; a row at or past its time counts
// for nothing.
func (s *Store) agyLimited(ctx context.Context, q sqlQuery, now time.Time) (bool, error) {
	var held bool
	err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM agy_limits WHERE account=? AND until>?)", agyAccount, now.UnixMilli()).Scan(&held)
	return held, err
}

// releaseAgyLimits ends the limits that have reset: it deletes their rows and, in the same transaction, queues a dispatch for every open
// agy session through the path a new input takes, for the streams that are connected; a stopped box is started by WakeStopped, which runs right after this in the same pass and keeps the paused-room rule. A session with a pending waking input starts; the rest stay idle. Nothing else of a
// held session was touched while it was held, so a Stop and an owner's post mean what they always do. A restart keeps the rows, and this
// is what releases them.
func (s *Store) releaseAgyLimits(ctx context.Context, now time.Time) error {
	var due bool
	if err := s.Read().QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM agy_limits WHERE until<=?)", now.UnixMilli()).Scan(&due); err != nil || !due {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM agy_limits WHERE until<=?", now.UnixMilli())
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT s.id FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.state='open' AND s.closed_at IS NULL AND b.harness=?", contract.HarnessAgy)
		if err != nil {
			return err
		}
		var ids []contract.SessionID
		for rows.Next() {
			var id contract.SessionID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			s.wakePending = append(s.wakePending, id)
		}
		return nil
	})
}
