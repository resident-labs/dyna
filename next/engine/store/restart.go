package store

import (
	"context"
	"database/sql"

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

// restartSnapshotSQL fences close and any change to the failed incarnation observed
// by the authorized caller before joining runtime work. No event grants deletion.
const restartSnapshotSQL = `SELECT EXISTS (SELECT 1 FROM boxes b JOIN sessions s ON s.id=b.session JOIN rooms r ON r.id=s.room WHERE b.id=? AND b.session=? AND coalesce(b.host,'')=? AND b.image=? AND b.updated_at=? AND b.reason=? AND b.slice=? AND b.created_at=? AND b.state='failed' AND b.want IN ('run','stop') AND s.state='open' AND s.closed_at IS NULL AND r.state!='deleting')`

func (s *Store) CanRestartSession(ctx context.Context, old box.Box) (current bool, err error) {
	if old.State != contract.BoxFailed {
		return false, nil
	}
	err = s.Read().QueryRowContext(ctx, restartSnapshotSQL, old.ID, old.Session, old.Host, old.Image, old.Updated.UnixMilli(), old.Reason, old.Slice, old.PlacedAt.UnixMilli()).Scan(&current)
	return current, err
}

// BoxLeftovers lists all known old allocations, including prior retry hosts.
// Their volume records remain until explicit destructive cleanup confirms them.
func (s *Store) BoxLeftovers(ctx context.Context, id contract.BoxID) ([]Leftover, error) {
	return queryAll(ctx, s.Read(), func(rows scanner) (l Leftover, err error) {
		err = rows.Scan(&l.Host, &l.Room, &l.Box, &l.Slice)
		return
	}, `SELECT host,room,box,slice FROM room_leftovers WHERE box=? ORDER BY host,room`, id)
}

// RestartSession commits a reset only after the API has synchronously removed the
// old host resources. This is persistence, not runtime deletion authorization.
func (s *Store) RestartSession(ctx context.Context, old box.Box, event, notice contract.Event) (bool, error) {
	if old.State != contract.BoxFailed {
		return false, nil
	}
	var restarted bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if s.volumeRemovals[old.ID] != nil {
			return nil
		}
		var current bool
		if err := tx.QueryRowContext(ctx, restartSnapshotSQL, old.ID, old.Session, old.Host, old.Image, old.Updated.UnixMilli(), old.Reason, old.Slice, old.PlacedAt.UnixMilli()).Scan(&current); err != nil || !current {
			return err
		}
		b := old
		// Deletion has completed on the OLD host, even if admission must detach it.
		err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT name FROM hosts WHERE name=? AND state='ready' AND create_problem=''),'')`, old.Host).Scan(&b.Host)
		if err != nil {
			return err
		}
		_, body, err := box.Transition(b, contract.BoxPending, "", event.Time)
		if err != nil {
			return err
		}
		body.Cause = event.Body.(contract.BoxChanged).Cause
		event.Body = body
		// Every known host must have confirmed destructive cleanup before reset.
		// The API retires each record only after that host succeeds; a newly
		// discovered debt fences this commit instead of silently forgetting it.
		var debts bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM room_leftovers WHERE box=?)`, old.ID).Scan(&debts); err != nil || debts {
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
		if err := s.restartNotice(ctx, tx, notice); err != nil {
			return err
		}
		if event.From.Kind == contract.PrincipalOwner {
			_, err := tx.ExecContext(ctx, "UPDATE deliveries SET immediate=1 WHERE to_kind='session' AND to_id=? AND seq=(SELECT seq FROM events WHERE id=?)", old.Session, notice.ID)
			return err
		}
		return nil
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
