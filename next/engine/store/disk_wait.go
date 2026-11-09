package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// NotifyDiskWaits uses the same box.changed reasons that supply the pending UI.
// The first disk event after the last departure from pending identifies a wait;
// its notice ID makes delivery durable and once-only across engine restarts.
func (s *Store) NotifyDiskWaits(ctx context.Context, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.id,s.name,s.parent,s.room,b.host,
 (SELECT min(e.seq) FROM events e WHERE e.room=s.room AND e.type='box.changed'
 AND json_extract(e.body,'$.box')=b.id AND json_extract(e.body,'$.to')='pending' AND json_extract(e.body,'$.reason')='disk'
 AND e.seq>coalesce((SELECT max(x.seq) FROM events x WHERE x.room=s.room AND x.type='box.changed' AND json_extract(x.body,'$.box')=b.id AND json_extract(x.body,'$.to')!='pending'),0))
 FROM boxes b JOIN sessions s ON s.id=b.session WHERE b.state='pending' AND b.reason='disk' AND b.host IS NOT NULL AND b.host!='' AND s.parent IS NOT NULL AND s.closed_at IS NULL`)
		if err != nil {
			return err
		}
		type wait struct {
			session contract.SessionID
			name    string
			parent  contract.SessionID
			room    contract.RoomID
			host    string
			seq     sql.NullInt64
		}
		var waits []wait
		for rows.Next() {
			var w wait
			if err := rows.Scan(&w.session, &w.name, &w.parent, &w.room, &w.host, &w.seq); err != nil {
				rows.Close()
				return err
			}
			waits = append(waits, w)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, w := range waits {
			if !w.seq.Valid {
				continue
			}
			id := fmt.Sprintf("disk-wait-%d", w.seq.Int64)
			var due bool
			if err := tx.QueryRowContext(ctx, `SELECT at<=? AND NOT EXISTS(SELECT 1 FROM events WHERE id=?) FROM events WHERE seq=?`, at.Add(-5*time.Minute).UnixMilli(), id, w.seq.Int64).Scan(&due); err != nil {
				return err
			}
			if !due {
				continue
			}
			body := contract.SessionNotice{Session: w.session, Text: fmt.Sprintf("%s (%s) has waited at least 5 minutes for disk space on host %s. Its box remains pending on that host; it has not been relocated because its volume may contain unpushed work.", w.name, w.session, w.host)}
			seq, err := s.AppendEvent(tx, contract.Event{ID: contract.EventID(id), Room: w.room, Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body})
			if err != nil {
				return err
			}
			if err := s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: w.parent}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
				return err
			}
		}
		return nil
	})
}
