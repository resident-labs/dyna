package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// PendingSpell uses durable lifecycle events, not updated_at (which changes on
// every retry). Reason changes do not begin another spell; leaving pending does.
func (s *Store) PendingSpell(ctx context.Context, b box.Box) (time.Time, string, error) {
	var boundary, since int64
	err := s.Read().QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM events WHERE room=? AND type='box.changed' AND json_extract(body,'$.box')=? AND json_extract(body,'$.to')!='pending'`, b.Room, b.ID).Scan(&boundary)
	if err != nil {
		return time.Time{}, "", err
	}
	err = s.Read().QueryRowContext(ctx, `SELECT coalesce(min(at),?) FROM events WHERE room=? AND type='box.changed' AND json_extract(body,'$.box')=? AND seq>? AND json_extract(body,'$.to')='pending'`, b.PlacedAt.UnixMilli(), b.Room, b.ID, boundary).Scan(&since)
	return time.UnixMilli(since), fmt.Sprintf("admission:%s:%d", b.ID, boundary), err
}

// SendAdmissionNotice fences a notice against placement while it was prepared.
func (s *Store) SendAdmissionNotice(ctx context.Context, b box.Box, event contract.Event, to contract.SessionID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var current bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM boxes WHERE id=? AND state='pending' AND updated_at=? AND reason=?)`, b.ID, b.Updated.UnixMilli(), b.Reason).Scan(&current); err != nil || !current {
			return err
		}
		body := event.Body.(contract.SessionNotice)
		sent, err := s.noticeSent(ctx, tx, event, body.ClientID)
		if err != nil || sent {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
	})
}
