package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// CantReceiveError refuses a child's post to a parent that cannot take it, naming who takes it instead.
type CantReceiveError struct{ Message string }

func (e *CantReceiveError) Error() string { return e.Message }

// receiver decides who can receive for a session now: the session itself, or else its nearest ancestor that can, ending at
// root. why says what stops the session itself, and is empty when it can receive. A session cannot receive while it closes
// or after, or while its box is failed, lost, or pending because it cannot be placed.
func receiver(ctx context.Context, tx *sql.Tx, id contract.SessionID) (to contract.SessionID, why string, err error) {
	for {
		var parent, state, boxState, reason string
		var want BoxWant
		var closed bool
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(s.parent,''),s.state,s.closed_at IS NOT NULL,COALESCE(b.state,''),COALESCE(b.reason,''),COALESCE(b.want,'') FROM sessions s LEFT JOIN boxes b ON b.session=s.id WHERE s.id=?", id).Scan(&parent, &state, &closed, &boxState, &reason, &want)
		if err != nil {
			return "", "", err
		}
		stop := cantReceive(state, closed, contract.BoxState(boxState), reason, want)
		if why == "" {
			why = stop
		}
		if stop == "" || parent == "" {
			return id, why, nil
		}
		id = contract.SessionID(parent)
	}
}

func cantReceive(state string, closed bool, box contract.BoxState, reason string, want BoxWant) string {
	detail := ""
	if reason != "" {
		detail = " (" + reason + ")"
	}
	switch {
	case closed:
		return "it is closed"
	case state == "closing" || want == WantClose || want == WantDiscard:
		return "it is closing"
	case box == contract.BoxFailed:
		return "its box failed" + detail
	case box == contract.BoxLost:
		return "its box was lost" + detail
	case box == contract.BoxPending && reason != "":
		return "its box can't be placed" + detail
	}
	return ""
}

// refuseDeadParent refuses a session's post that reaches its own parent while the parent cannot receive. Nothing waits for
// a parent that may never come back.
func refuseDeadParent(ctx context.Context, tx *sql.Tx, from contract.Principal, deliveries []contract.Delivery) error {
	if from.Kind != contract.PrincipalSession {
		return nil
	}
	var parent, parentName string
	err := tx.QueryRowContext(ctx, "SELECT p.id,p.name FROM sessions c JOIN sessions p ON p.id=c.parent WHERE c.id=?", from.Session).Scan(&parent, &parentName)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if d.To.Kind != contract.PrincipalSession || string(d.To.Session) != parent {
			continue
		}
		to, why, err := receiver(ctx, tx, d.To.Session)
		if err != nil || why == "" {
			return err
		}
		if string(to) == parent {
			return &CantReceiveError{Message: fmt.Sprintf("your parent %s can't receive: %s. Nothing above it can take this, so send it again once it's back.", parentName, why)}
		}
		var name string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM sessions WHERE id=?", to).Scan(&name); err != nil {
			return err
		}
		return &CantReceiveError{Message: fmt.Sprintf("your parent %s can't receive: %s. Deliver to %s (mention %s) until it's back.", parentName, why, name, name)}
	}
	return nil
}

// dropUndelivered discards what a closing session never read. Its posts stay in the room, but nobody acts on them: replaying
// hours-old inputs to an ancestor only floods it with superseded work. The next session up that can receive gets one short
// notice instead.
func (s *Store) dropUndelivered(ctx context.Context, tx *sql.Tx, id contract.SessionID, room contract.RoomID, at time.Time) error {
	var dropped int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deliveries d WHERE d.to_kind='session' AND d.to_id=? AND d.state IN ('pending','taken') AND NOT `+jobRunningNotice+` AND NOT `+boxUpdateNotice, id).Scan(&dropped); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM deliveries WHERE to_kind='session' AND to_id=? AND state IN ('pending','taken')", id); err != nil {
		return err
	}
	to, _, err := receiver(ctx, tx, id)
	if err != nil || dropped == 0 || to == id {
		return err
	}
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM sessions WHERE id=?", id).Scan(&name); err != nil {
		return err
	}
	text := fmt.Sprintf("%s closed; %d undelivered messages to it were dropped.", name, dropped)
	if dropped == 1 {
		text = fmt.Sprintf("%s closed; 1 undelivered message to it was dropped.", name)
	}
	body := contract.SessionNotice{Session: id, Text: text}
	seq, err := s.appendEngineEvent(tx, room, at, body)
	if err != nil {
		return err
	}
	return s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
}
