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

// agyAccount is the one Antigravity account every neon session uses. A second account would make it a column of the box; until then it is
// a name, and the table already holds a row per account.
const agyAccount = "main"

// limitAgy records that the account's usage limit resets after seconds, the reset an agy turn's failure stated, and keeps the later of two
// resets. It tells the room once per limit: a limit that was already in force is not told again, whatever the turns still ending on it
// say, since each of them states its own reset a second or two apart. The notice wakes root through the default subscription, so the owner
// hears of it once, whatever the number of sessions the limit holds.
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
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	notice := contract.NoticePosted{Text: fmt.Sprintf("Antigravity quota reached; neon sessions resume at %s UTC", until.UTC().Format("15:04"))}
	_, err = s.AppendEvent(tx, contract.Event{ID: contract.EventID("ev_" + hex.EncodeToString(id[:])), Room: event.Room, Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: notice})
	return err
}

// holdForAgyLimit keeps an agy session from starting a turn while its account's limit stands: asking Google again before the reset only
// fails again. Its inputs stay pending, without their wake, and its retry time is the reset, so the sweep that wakes model retries wakes it
// then, as it does a session whose own turn failed; a later retry the session already has is kept. A row at or past its time counts for nothing.
func (s *Store) holdForAgyLimit(ctx context.Context, tx *sql.Tx, session contract.SessionID, now time.Time) (bool, error) {
	var until int64
	err := tx.QueryRowContext(ctx, "SELECT until FROM agy_limits WHERE account=? AND until>?", agyAccount, now.UnixMilli()).Scan(&until)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=? WHERE id=? AND model_retry_at<?1", until, session); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE deliveries SET wake=0 WHERE to_kind=? AND to_id=? AND state='pending' AND wake=1", contract.PrincipalSession, session)
	return err == nil, err
}
