package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// ChainError refuses a session's message to a session outside its chain of command. Nothing is queued.
type ChainError struct{ Message string }

func (e *ChainError) Error() string { return e.Message }

// receiver decides who receives for a session now, and the chain of command is its one rule. A session may address its
// parent and any session under its parent: its siblings, its own children and theirs, and so on down. Root, with no
// parent, may address any session under it. A crew may address only its parent. reach is false for anyone else, such as
// a grandparent or another branch. The owner and the engine may address any session. What a session sends its parent goes, while the parent cannot receive, to the parent's nearest
// ancestor that can, ending at root; why then says what stops the parent. Anything else goes where it was sent.
//
// A session cannot receive while it closes or after, or while its box is failed, lost, or pending for a reason that lasts
// until someone acts: no host its room allows, its room's limit, a size no host has, or a pool that fails. A box that waits
// for CPU, memory, disk or a pool's next node starts by itself, and the parent reads its messages then.
func receiver(ctx context.Context, tx rowQuerier, from contract.Principal, to contract.SessionID) (got contract.SessionID, why string, reach bool, err error) {
	if from.Kind != contract.PrincipalSession {
		return to, "", true, nil
	}
	var parent string
	var crew, under bool
	// under: the top of from's reach, its parent or else from itself as root, is an ancestor of to.
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE f AS (SELECT COALESCE(s.parent,'') AS parent,EXISTS(SELECT 1 FROM workflow_runs w WHERE w.session=s.id) AS crew FROM sessions s WHERE s.id=?),
		up(id) AS (SELECT parent FROM sessions WHERE id=? AND parent IS NOT NULL UNION SELECT p.parent FROM sessions p JOIN up ON p.id=up.id WHERE p.parent IS NOT NULL)
		SELECT f.parent,f.crew,EXISTS(SELECT 1 FROM up WHERE up.id=CASE f.parent WHEN '' THEN ? ELSE f.parent END) FROM f`, from.Session, to, from.Session).Scan(&parent, &crew, &under)
	if err != nil {
		return "", "", false, err
	}
	if parent == "" || string(to) != parent {
		return to, "", under && !crew, nil
	}
	for id := to; ; {
		var up, state, boxState, reason string
		var want BoxWant
		var closed bool
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(s.parent,''),s.state,s.closed_at IS NOT NULL,COALESCE(b.state,''),COALESCE(b.reason,''),COALESCE(b.want,'') FROM sessions s LEFT JOIN boxes b ON b.session=s.id WHERE s.id=?", id).Scan(&up, &state, &closed, &boxState, &reason, &want)
		if err != nil {
			return "", "", false, err
		}
		stop := cantReceive(state, closed, contract.BoxState(boxState), reason, want)
		if why == "" {
			why = stop
		}
		if stop == "" || up == "" {
			return id, why, true, nil
		}
		id = contract.SessionID(up)
	}
}

var lasting = []box.PendingReason{box.PendingRoomHost, box.PendingRoomLimit, box.PendingInvalidSize, box.PendingPoolFailed}

func cantReceive(state string, closed bool, boxState contract.BoxState, reason string, want BoxWant) string {
	switch {
	case closed:
		return "closed"
	case state == "closing" || want == WantClose || want == WantDiscard:
		return "closing"
	case boxState == contract.BoxFailed:
		return "box failed"
	case boxState == contract.BoxLost:
		return "box lost"
	case boxState == contract.BoxPending && slices.Contains(lasting, box.PendingReason(reason)):
		return "box can't be placed"
	}
	return ""
}

// reroute sends d where receiver says. A delivery that goes past the session it was meant for keeps that session and why.
func reroute(ctx context.Context, tx *sql.Tx, from contract.Principal, d *contract.Delivery) (reach bool, err error) {
	if d.To.Kind != contract.PrincipalSession {
		return true, nil
	}
	got, why, reach, err := receiver(ctx, tx, from, d.To.Session)
	if err != nil || !reach {
		return reach, err
	}
	if got != d.To.Session {
		d.MeantFor, d.Why, d.To.Session = d.To.Session, why, got
	}
	return true, nil
}

// routePost refuses a post that reaches a session outside its author's chain of command, and sends the rest where receiver
// says.
func routePost(ctx context.Context, tx *sql.Tx, from contract.Principal, deliveries []contract.Delivery) error {
	for i := range deliveries {
		reach, err := reroute(ctx, tx, from, &deliveries[i])
		if err != nil {
			return err
		}
		if !reach {
			return chainRefusal(ctx, tx, from.Session)
		}
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// chainRefusal is the one line that refuses a session's message outside its chain of command.
func chainRefusal(ctx context.Context, q rowQuerier, session contract.SessionID) error {
	var parent string
	var crew bool
	err := q.QueryRowContext(ctx, "SELECT COALESCE((SELECT p.name FROM sessions p WHERE p.id=s.parent),''),EXISTS(SELECT 1 FROM workflow_runs w WHERE w.session=s.id) FROM sessions s WHERE s.id=?", session).Scan(&parent, &crew)
	if err != nil {
		return err
	}
	switch {
	case crew:
		return &ChainError{Message: fmt.Sprintf("a workflow can message only its parent (%s).", parent)}
	case parent == "":
		return &ChainError{Message: "you can message sessions under you; a post that mentions no session goes to the owner."}
	}
	return &ChainError{Message: fmt.Sprintf("you can message your parent (%s) and sessions under it; your parent passes anything else on.", parent)}
}

// OutsideChain refuses a session's mention that by its form names no one in the session's chain of command: a session in
// another room.
func (s *Store) OutsideChain(ctx context.Context, session contract.SessionID) error {
	return chainRefusal(ctx, s.Read(), session)
}

// Reaches reports whether from may address to now, by receiver's rule.
func (s *Store) Reaches(ctx context.Context, from contract.Principal, to contract.SessionID) (bool, error) {
	_, _, reach, err := receiver(ctx, s.Read(), from, to)
	return reach, err
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
	var name, parent string
	if err := tx.QueryRowContext(ctx, "SELECT name,COALESCE(parent,'') FROM sessions WHERE id=?", id).Scan(&name, &parent); err != nil || dropped == 0 || parent == "" {
		return err
	}
	to, _, _, err := receiver(ctx, tx, contract.Principal{Kind: contract.PrincipalSession, Session: id}, contract.SessionID(parent))
	if err != nil {
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
