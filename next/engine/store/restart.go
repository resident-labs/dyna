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
			if sent, err := s.noticeSent(ctx, tx, notice, body.ClientID); err != nil || sent {
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
		// Record the old host's partial resources before detaching its quarantined box.
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO room_leftovers(host,room,box,slice) SELECT b.host,s.room,b.id,b.slice FROM boxes b JOIN sessions s ON s.id=b.session JOIN hosts h ON h.name=b.host WHERE b.id=? AND h.create_problem!=''`, b.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE boxes SET state='pending',want='run',create_retry_host='',failures=0,due_at=0,image='',host=CASE WHEN EXISTS (SELECT 1 FROM hosts h WHERE h.name=boxes.host AND h.state='ready' AND h.create_problem='') THEN host ELSE NULL END,reason='',updated_at=? WHERE id=? AND state='failed'`, event.Time.UnixMilli(), b.ID)
		if err != nil {
			return err
		}
		if _, err := s.AppendEvent(tx, event); err != nil {
			return err
		}
		restarted = true
		return s.restartNotice(ctx, tx, notice)
	})
	return restarted, err
}

// noticeSent says whether the engine already wrote a notice with this client ID in the room.
func (s *Store) noticeSent(ctx context.Context, tx *sql.Tx, notice contract.Event, clientID string) (sent bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE room=? AND from_kind=? AND from_id='' AND client_id=?)`, notice.Room, notice.From.Kind, clientID).Scan(&sent)
	return sent, err
}

// openGoalSQL asks whether an open session holds a goal that is not done.
const openGoalSQL = `SELECT EXISTS (SELECT 1 FROM goals g JOIN sessions s ON s.id=g.session WHERE g.session=? AND g.state='active' AND s.closed_at IS NULL)`

// HasOpenGoal says whether a session holds a goal that is not done.
func (s *Store) HasOpenGoal(ctx context.Context, id contract.SessionID) (open bool, err error) {
	err = s.Read().QueryRowContext(ctx, openGoalSQL, id).Scan(&open)
	return open, err
}

// NoticeRestarted tells a session its box started again after it stopped.
func (s *Store) NoticeRestarted(ctx context.Context, notice contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error { return s.restartNotice(ctx, tx, notice) })
}

// restartNotice stores a SessionRestarted notice for its session, once per client ID. The goal state is read in the same
// transaction and stored in the notice, so the text and the wake agree: with an open goal the notice starts a turn, and without
// one it waits for the session's next input.
func (s *Store) restartNotice(ctx context.Context, tx *sql.Tx, notice contract.Event) error {
	body := notice.Body.(contract.SessionRestarted)
	if body.ClientID != "" {
		if sent, err := s.noticeSent(ctx, tx, notice, body.ClientID); err != nil || sent {
			return err
		}
	}
	if err := tx.QueryRowContext(ctx, openGoalSQL, body.Session).Scan(&body.OpenGoal); err != nil {
		return err
	}
	notice.Body = body
	seq, err := s.AppendEvent(tx, notice)
	if err != nil {
		return err
	}
	return s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: body.Session}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: body.OpenGoal || body.Refreshed})
}
