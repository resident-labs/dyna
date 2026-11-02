package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// SendSessionNotice stores a notice for its session, which reads it with its next turn.
func (s *Store) SendSessionNotice(ctx context.Context, notice contract.Event, id contract.SessionID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		seq, err := s.AppendEvent(tx, notice)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, notice.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: id}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: false})
	})
}

// RestartSession resets a failed box for admission and publishes its transition.
func (s *Store) RestartSession(ctx context.Context, id contract.SessionID, event, notice contract.Event) (bool, error) {
	var restarted bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		b := box.Box{Session: id, State: contract.BoxFailed}
		err := tx.QueryRowContext(ctx, `SELECT b.id,coalesce((SELECT h.name FROM hosts h WHERE h.name=b.host AND h.state='ready'),'') FROM boxes b JOIN sessions s ON s.id=b.session WHERE s.id=? AND s.state='open' AND s.closed_at IS NULL AND b.state='failed'`, id).Scan(&b.ID, &b.Host)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, body, err := box.Transition(b, contract.BoxPending, "", event.Time)
		if err != nil {
			return err
		}
		body.Cause = event.Body.(contract.BoxChanged).Cause
		event.Body = body
		_, err = tx.ExecContext(ctx, `UPDATE boxes SET state='pending',want='run',failures=0,due_at=0,image='',host=CASE WHEN EXISTS (SELECT 1 FROM hosts h WHERE h.name=boxes.host AND h.state='ready') THEN host ELSE NULL END,reason='',updated_at=? WHERE id=? AND state='failed'`, event.Time.UnixMilli(), b.ID)
		if err != nil {
			return err
		}
		if _, err := s.AppendEvent(tx, event); err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, notice)
		if err != nil {
			return err
		}
		if err := s.InsertDelivery(ctx, tx, seq, notice.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: id}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: false}); err != nil {
			return err
		}
		restarted = true
		return nil
	})
	return restarted, err
}
