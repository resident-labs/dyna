package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

type createRetryEligibility uint8

const (
	createRetryUnknown createRetryEligibility = iota
	createRetryUnbound
	createRetrySameHost
	createRetryUsed
)

// allocationRetryEligibility reads structured provenance, never a human reason. StartSession writes an initial pending
// BoxChanged in the box's transaction and assigns its Host only for sameHost. An explicit owner restart ends that initial
// affinity. Events survive as long as the room's boxes; absent legacy/import provenance is unknown, not permission to move.
// Readiness and turns are measured since that allocation boundary, not created_at, which ordinary admission can update.
func allocationRetryEligibility(ctx context.Context, tx *sql.Tx, id contract.BoxID, room contract.RoomID) (createRetryEligibility, error) {
	var host contract.HostName
	var restarted bool
	var seq, at int64
	err := tx.QueryRowContext(ctx, `SELECT coalesce(json_extract(CAST(body AS TEXT),'$.host'),''),coalesce(json_extract(CAST(body AS TEXT),'$.from'),'')='failed',seq,at FROM events WHERE room=? AND type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')=? AND json_extract(CAST(body AS TEXT),'$.to')='pending' AND (coalesce(json_extract(CAST(body AS TEXT),'$.from'),'')='' OR (from_kind='owner' AND json_extract(CAST(body AS TEXT),'$.from')='failed')) ORDER BY seq DESC LIMIT 1`, room, id).Scan(&host, &restarted, &seq, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return createRetryUnknown, nil
	}
	if err != nil {
		return createRetryUnknown, err
	}
	if !restarted && host != "" {
		return createRetrySameHost, nil
	}
	var used bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE room=? AND seq>? AND type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')=? AND json_extract(CAST(body AS TEXT),'$.to')='ready') OR EXISTS(SELECT 1 FROM turns WHERE session=(SELECT session FROM boxes WHERE id=?) AND started_at>=?)`, room, seq, id, id, at).Scan(&used)
	if err != nil {
		return createRetryUnknown, err
	}
	if used {
		return createRetryUsed, nil
	}
	return createRetryUnbound, nil
}

// A quarantine can detach only allocations known to be unpinned and unused. The same eligibility boundary owns ordinary
// waiting creates and a terminal failure's retry; sameHost must not disappear when some other allocation fails.
func detachWaitingCreates(ctx context.Context, tx *sql.Tx, host contract.HostName) error {
	rows, err := tx.QueryContext(ctx, `SELECT b.id,s.room,b.slice FROM boxes b JOIN sessions s ON s.id=b.session WHERE b.host=? AND b.state='pending' AND b.image=''`, host)
	if err != nil {
		return err
	}
	type waiting struct {
		id    contract.BoxID
		room  contract.RoomID
		slice string
	}
	var waitingBoxes []waiting
	for rows.Next() {
		var b waiting
		if err := rows.Scan(&b.id, &b.room, &b.slice); err != nil {
			rows.Close()
			return err
		}
		waitingBoxes = append(waitingBoxes, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range waitingBoxes {
		eligibility, err := allocationRetryEligibility(ctx, tx, b.id, b.room)
		if err != nil {
			return err
		}
		if eligibility != createRetryUnbound {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO room_leftovers(host,room,box,slice) VALUES(?,?,?,?)`, host, b.room, b.id, b.slice); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE boxes SET host=NULL WHERE id=? AND host=? AND state='pending' AND image=''`, b.id, host); err != nil {
			return err
		}
	}
	return nil
}
