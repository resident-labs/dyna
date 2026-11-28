package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// agyKey is the key of the active account's row in agy_limits: its id once an account is active, and "main", the one signed-in account
// from before accounts were added, until then. Importing the signed-in account renames the "main" row to its id, and a switch makes the
// other account's row, if it has one, the one that applies.
const agyKey = `COALESCE(NULLIF((SELECT active FROM agy_account),''),'main')`

// limitAgy records that the account's usage limit resets after seconds, the reset an agy turn's failure stated, and keeps the later of two
// resets. It tells the room once per limit: a limit that was already in force is not told again, whatever the turns still ending on it
// say, since each of them states its own reset a second or two apart. The notice wakes root through the default subscription, so the owner
// hears of it once, whatever the number of sessions the limit holds.
func (s *Store) limitAgy(ctx context.Context, tx *sql.Tx, event contract.Event, seconds int64) error {
	until := event.Time.Add(time.Duration(seconds) * time.Second)
	var previous int64
	err := tx.QueryRowContext(ctx, "SELECT until FROM agy_limits WHERE account="+agyKey).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO agy_limits(account,until) SELECT "+agyKey+",?1 WHERE true ON CONFLICT(account) DO UPDATE SET until=MAX(until,?1)", until.UnixMilli()); err != nil {
		return err
	}
	if previous > event.Time.UnixMilli() {
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	notice := contract.NoticePosted{Text: fmt.Sprintf("Antigravity quota reached; neon sessions resume at %s UTC", until.UTC().Format("15:04"))}
	_, err = s.AppendEvent(tx, contract.Event{ID: contract.EventID("ev_" + hex.EncodeToString(id[:])), Room: event.Room, Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: notice})
	return err
}

// agyLimited says no agy turn or compaction may go out now: the active account's usage limit stands at now (asking Google again before
// the reset only fails again), or an account switch is held, whose token swap must not meet a running agy. It is the one check that decides
// this, for dispatch, the idle compaction and the retry wake alike, and its state is two rows; a limit row at or past its time counts for
// nothing.
func (s *Store) agyLimited(ctx context.Context, q sqlQuery, now time.Time) (bool, error) {
	var held bool
	err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM agy_limits WHERE account="+agyKey+" AND until>?) OR COALESCE((SELECT held FROM agy_account),0)=1", now.UnixMilli()).Scan(&held)
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
		return s.wakeAgySessions(ctx, tx)
	})
}

// wakeAgySessions queues a dispatch for every open agy session, for the streams that are connected, when what held them ends.
func (s *Store) wakeAgySessions(ctx context.Context, tx *sql.Tx) error {
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
	s.wakePending = append(s.wakePending, ids...)
	return nil
}
