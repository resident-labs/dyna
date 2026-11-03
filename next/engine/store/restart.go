package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// SendSessionNotice stores a notice for its session, which reads it with its next turn. A notice with a client ID goes once,
// and a restart does not change that.
func (s *Store) SendSessionNotice(ctx context.Context, notice contract.Event, id contract.SessionID) error {
	return s.sendNotice(ctx, notice, id, false)
}

// WakeWithNotice is SendSessionNotice with a wake, which starts a turn with the notice, and the box first if it is stopped.
func (s *Store) WakeWithNotice(ctx context.Context, notice contract.Event, id contract.SessionID) error {
	return s.sendNotice(ctx, notice, id, true)
}

func (s *Store) sendNotice(ctx context.Context, notice contract.Event, id contract.SessionID, wake bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if body, ok := notice.Body.(contract.SessionNotice); ok && body.ClientID != "" {
			var sent bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE room=? AND from_kind=? AND from_id='' AND client_id=?)`, notice.Room, notice.From.Kind, body.ClientID).Scan(&sent); err != nil || sent {
				return err
			}
		}
		seq, err := s.AppendEvent(tx, notice)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, notice.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: id}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: wake})
	})
}

// RestartSession resets a failed box for admission and publishes its transition.
func (s *Store) RestartSession(ctx context.Context, id contract.SessionID, event, notice contract.Event) (bool, error) {
	var restarted bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		b := box.Box{Session: id, State: contract.BoxFailed}
		err := tx.QueryRowContext(ctx, `SELECT b.id,coalesce((SELECT h.name FROM hosts h WHERE h.name=b.host AND h.state='ready' AND h.create_problem=''),'') FROM boxes b JOIN sessions s ON s.id=b.session WHERE s.id=? AND s.state='open' AND s.closed_at IS NULL AND b.state='failed'`, id).Scan(&b.ID, &b.Host)
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
		_, err = tx.ExecContext(ctx, `UPDATE boxes SET state='pending',want='run',failures=0,due_at=0,image='',host=CASE WHEN EXISTS (SELECT 1 FROM hosts h WHERE h.name=boxes.host AND h.state='ready' AND h.create_problem='') THEN host ELSE NULL END,reason='',updated_at=? WHERE id=? AND state='failed'`, event.Time.UnixMilli(), b.ID)
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
