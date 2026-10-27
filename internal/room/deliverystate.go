package room

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// The delivery rows hold how far each delivery got (post_deliveries.state),
// and the turn that claimed it (claimed_turn). These five writes are the only
// code that changes that state, and each runs as one transaction. Before the
// rows held it, the same state was one JSON blob per session in
// session_queues; these writes give the same rules:
//
//   - A post cancelled or dismissed stays that way. A claim never revives it.
//   - A claim names its turn. The same turn claiming a post again is a repeat,
//     and returns the post as claimed again.
//   - A later turn takes over a post a finished turn claimed and did not
//     deliver, which is what a write of the queue blob did.
//   - A cancellation only drops a pending delivery: queued, or imported from a
//     release that could not tell a delivered post from a pending one. Once a
//     turn claimed a post, the owner cannot cancel it.
//   - A dismissal drops a queued or a claimed delivery. The caller checks
//     first that the post is pending and that no turn is running (see
//     subagent's DismissPendingPost), and the turn lock makes that race-free.
//
// Callers hold the session's gate lock where they did before, so a read and
// the write that follows it see the same state.

// DeliverySet names one session's delivery rows in one room: the session_id
// keys that name the session (see DeliveryKeys) and the room they live in.
type DeliverySet struct {
	RoomID string
	Keys   []string
}

// Claimed reports whether the set can name any row at all.
func (set DeliverySet) Claimed() bool { return set.RoomID != "" && len(set.Keys) > 0 }

// inClause lists keys for one IN clause: "?" for one key, "?,?" for two.
func inClause(keys []string) string { return "?" + strings.Repeat(",?", len(keys)-1) }

// keyArgs appends the set's keys to args.
func keyArgs(args []any, keys []string) []any {
	for _, key := range keys {
		args = append(args, key)
	}
	return args
}

// ClaimDeliveries claims the deliveries of postIDs for turn in one
// transaction, then publishes ev. It returns the post IDs whose rows now name
// that turn. A post cancelled or dismissed is not claimed; see
// ClaimDeliveriesIn for the row a session without one gets.
func (s *Store) ClaimDeliveries(ctx context.Context, ev hub.Event, set DeliverySet, postIDs []string, turn int) ([]string, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	var claimed []string
	err = state.For(db).Write(ctx, ev, func(conn *sql.Conn) error {
		var err error
		claimed, err = ClaimDeliveriesIn(ctx, conn, set, postIDs, turn)
		return err
	})
	return claimed, err
}

// ClaimDeliveriesIn claims the deliveries of postIDs for turn on conn, inside
// the caller's transaction. The caller publishes the change, or writes it for
// a reply it commits in the same transaction (see subagent's input claim).
//
// A post the session has no row for gets one, claimed by this turn: a session
// that inherits another inbox's posts claims them there, and that row is its
// record that this turn took them. It is quiet, because the wake, if any, came
// with the origin's row.
func ClaimDeliveriesIn(ctx context.Context, conn *sql.Conn, set DeliverySet, postIDs []string, turn int) ([]string, error) {
	if turn <= 0 {
		return nil, errors.New("a delivery claim needs a turn")
	}
	if !set.Claimed() || len(postIDs) == 0 {
		return nil, nil
	}
	at, err := deliveryWriteTime(ctx, conn)
	if err != nil {
		return nil, err
	}
	where := " WHERE room_id=? AND post_id=? AND session_id IN (" + inClause(set.Keys) +
		") AND state NOT IN ('cancelled','dismissed')"
	query := "UPDATE post_deliveries SET state='claimed', claimed_turn=?, updated_at=?" + where
	claimed := make([]string, 0, len(postIDs))
	for _, postID := range postIDs {
		args := []any{turn, at, set.RoomID, postID}
		args = keyArgs(args, set.Keys)
		n, err := execCount(ctx, conn, query, args...)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			if ok, err := addClaim(ctx, conn, set, postID, turn, at); err != nil {
				return nil, err
			} else if !ok {
				continue
			}
		}
		claimed = append(claimed, postID)
	}
	return claimed, nil
}

// addClaim gives a session that has no row for postID a claimed one, and
// reports false when its row already holds a final state the claim cannot
// take.
func addClaim(ctx context.Context, conn *sql.Conn, set DeliverySet, postID string, turn int, at int64) (bool, error) {
	countArgs := []any{set.RoomID, postID}
	countArgs = keyArgs(countArgs, set.Keys)
	var rows int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM post_deliveries WHERE room_id=? AND post_id=? AND session_id IN ("+inClause(set.Keys)+")", countArgs...).Scan(&rows); err != nil {
		return false, err
	}
	if rows > 0 {
		return false, nil
	}
	mode, err := deliveryModeOfPost(ctx, conn, set.RoomID, postID)
	if err != nil {
		return false, err
	}
	result, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO post_deliveries(room_id, post_id, session_id, mode, category, state, claimed_turn, updated_at)
VALUES(?, ?, ?, ?, '', 'claimed', ?, ?)`, set.RoomID, postID, set.Keys[0], string(mode), turn, at)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// deliveryModeOfPost is the mode a delivery row this API adds carries: the mode
// of the post's stored deliveries, so the added row never changes whether the
// post asks its reader for a turn. The wake decision was made when the post was
// written; a session that inherits a post inherits that decision with it. A post
// with no delivery row at all is quiet.
func deliveryModeOfPost(ctx context.Context, conn *sql.Conn, roomID, postID string) (DeliveryMode, error) {
	var mode string
	err := conn.QueryRowContext(ctx, `SELECT mode FROM post_deliveries WHERE room_id=? AND post_id=?
ORDER BY mode='wake' DESC LIMIT 1`, roomID, postID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryQuiet, nil
	}
	if err != nil {
		return "", err
	}
	return DeliveryMode(mode), nil
}

// CoverDeliveries cancels the deliveries of postIDs that a held prompt
// already covered, and returns the posts it cancelled. The startup input read
// uses it for the notes the held prompt carried: a turn claimed them once, and
// the next read must not deliver them again. It cancels a claimed delivery,
// which ClaimDeliveries and CancelDelivery both refuse, and an imported one,
// which a previous release's index import wrote before a turn could claim it.
func (s *Store) CoverDeliveries(ctx context.Context, ev hub.Event, set DeliverySet, postIDs []string) ([]string, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	var covered []string
	err = state.For(db).Write(ctx, ev, func(conn *sql.Conn) error {
		var err error
		covered, err = CoverDeliveriesIn(ctx, conn, set, postIDs)
		return err
	})
	return covered, err
}

// CoverDeliveriesIn is CoverDeliveries on conn, inside the caller's
// transaction.
func CoverDeliveriesIn(ctx context.Context, conn *sql.Conn, set DeliverySet, postIDs []string) ([]string, error) {
	if !set.Claimed() || len(postIDs) == 0 {
		return nil, nil
	}
	at, err := deliveryWriteTime(ctx, conn)
	if err != nil {
		return nil, err
	}
	query := "UPDATE post_deliveries SET state='cancelled', updated_at=? WHERE room_id=? AND post_id=?" +
		" AND session_id IN (" + inClause(set.Keys) + ") AND state IN ('queued','claimed','failed','imported')"
	covered := make([]string, 0, len(postIDs))
	for _, postID := range postIDs {
		args := []any{at, set.RoomID, postID}
		args = keyArgs(args, set.Keys)
		n, err := execCount(ctx, conn, query, args...)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			covered = append(covered, postID)
		}
	}
	return covered, nil
}

// CancelDelivery cancels a queued delivery of postID, then publishes ev. It
// reports whether it cancelled one: a delivery a turn already claimed stays
// claimed, so the owner cannot cancel input the turn may have read.
func (s *Store) CancelDelivery(ctx context.Context, ev hub.Event, set DeliverySet, postID string) (bool, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return false, err
	}
	var cancelled bool
	err = state.For(db).Write(ctx, ev, func(conn *sql.Conn) error {
		var err error
		cancelled, err = CancelDeliveryIn(ctx, conn, set, postID)
		return err
	})
	return cancelled, err
}

// CancelDeliveryIn is CancelDelivery on conn, inside the caller's transaction.
func CancelDeliveryIn(ctx context.Context, conn *sql.Conn, set DeliverySet, postID string) (bool, error) {
	if !set.Claimed() {
		return false, nil
	}
	at, err := deliveryWriteTime(ctx, conn)
	if err != nil {
		return false, err
	}
	args := []any{at, set.RoomID, postID}
	args = keyArgs(args, set.Keys)
	n, err := execCount(ctx, conn, "UPDATE post_deliveries SET state='cancelled', updated_at=? WHERE room_id=? AND post_id=?"+
		" AND session_id IN ("+inClause(set.Keys)+") AND state IN ('queued','imported')", args...)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DismissDelivery dismisses the delivery of postID, then publishes ev. It
// reports whether it dismissed one: a delivery already delivered stays
// delivered.
func (s *Store) DismissDelivery(ctx context.Context, ev hub.Event, set DeliverySet, postID string) (bool, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return false, err
	}
	var dismissed bool
	err = state.For(db).Write(ctx, ev, func(conn *sql.Conn) error {
		var err error
		dismissed, err = DismissDeliveryIn(ctx, conn, set, postID)
		return err
	})
	return dismissed, err
}

// DismissDeliveryIn is DismissDelivery on conn, inside the caller's
// transaction. It reports whether the session now holds a dismissed delivery
// for the post.
//
// A post the session has no row for, such as one it inherits from another
// session's inbox, gets one: the owner named this session's dismissal, and
// every origin's later read must see it. The row copies the post's delivery
// mode, so it changes nothing about who the post wakes, and it is dismissed, so
// no reader takes it for input.
func DismissDeliveryIn(ctx context.Context, conn *sql.Conn, set DeliverySet, postID string) (bool, error) {
	if !set.Claimed() {
		return false, nil
	}
	at, err := deliveryWriteTime(ctx, conn)
	if err != nil {
		return false, err
	}
	args := []any{at, set.RoomID, postID}
	args = keyArgs(args, set.Keys)
	n, err := execCount(ctx, conn, "UPDATE post_deliveries SET state='dismissed', updated_at=? WHERE room_id=? AND post_id=?"+
		" AND session_id IN ("+inClause(set.Keys)+") AND state IN ('queued','claimed','imported')", args...)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	// Either the session has no row for this post, or its row already holds a
	// final state. Only the first case adds a row.
	var rows int
	countArgs := []any{set.RoomID, postID}
	countArgs = keyArgs(countArgs, set.Keys)
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM post_deliveries WHERE room_id=? AND post_id=? AND session_id IN ("+inClause(set.Keys)+")", countArgs...).Scan(&rows); err != nil {
		return false, err
	}
	if rows > 0 {
		return false, nil
	}
	mode, err := deliveryModeOfPost(ctx, conn, set.RoomID, postID)
	if err != nil {
		return false, err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO post_deliveries(room_id, post_id, session_id, mode, category, state, claimed_turn, updated_at)
VALUES(?, ?, ?, ?, '', 'dismissed', 0, ?)`, set.RoomID, postID, set.Keys[0], string(mode), at)
	return err == nil, err
}

// MarkDelivered records that the recipient got the deliveries of postIDs,
// then publishes ev. It returns the post IDs whose rows changed. A
// cancellation or a dismissal is never overwritten: those rows say the post
// must not reach the recipient.
func (s *Store) MarkDelivered(ctx context.Context, set DeliverySet, postIDs []string) ([]string, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	var marked []string
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		var err error
		marked, err = MarkDeliveredIn(ctx, conn, set, postIDs)
		return err
	})
	return marked, err
}

// MarkDeliveredIn is MarkDelivered on conn, inside the caller's transaction.
func MarkDeliveredIn(ctx context.Context, conn *sql.Conn, set DeliverySet, postIDs []string) ([]string, error) {
	if !set.Claimed() || len(postIDs) == 0 {
		return nil, nil
	}
	at, err := deliveryWriteTime(ctx, conn)
	if err != nil {
		return nil, err
	}
	query := "UPDATE post_deliveries SET state='delivered', updated_at=? WHERE room_id=? AND post_id=?" +
		" AND session_id IN (" + inClause(set.Keys) + ") AND state NOT IN ('cancelled','dismissed','delivered')"
	marked := make([]string, 0, len(postIDs))
	for _, postID := range postIDs {
		args := []any{at, set.RoomID, postID}
		args = keyArgs(args, set.Keys)
		n, err := execCount(ctx, conn, query, args...)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			marked = append(marked, postID)
		}
	}
	return marked, nil
}

// NoteDeliveries records how a turn took the deliveries of postIDs, and when.
// A woke row stays woke: a later note must not call the cause a batched post.
// deliveredAt is a clock time. The row version still moves on its own.
func (s *Store) NoteDeliveries(ctx context.Context, ev hub.Event, set DeliverySet, postIDs []string, how DeliveryHow, turn int, deliveredAt int64) error {
	if how == "" || !set.Claimed() || len(postIDs) == 0 {
		return nil
	}
	db, err := s.postIndexDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, ev, func(conn *sql.Conn) error {
		return NoteDeliveriesIn(ctx, conn, set, postIDs, how, turn, deliveredAt)
	})
}

// NoteDeliveriesIn is NoteDeliveries on conn, inside the caller's transaction.
func NoteDeliveriesIn(ctx context.Context, conn *sql.Conn, set DeliverySet, postIDs []string, how DeliveryHow, turn int, deliveredAt int64) error {
	if how == "" || !set.Claimed() || len(postIDs) == 0 {
		return nil
	}
	version, err := deliveryWriteTime(ctx, conn)
	if err != nil {
		return err
	}
	query := "UPDATE post_deliveries SET how=?, delivered_at=CASE WHEN delivered_at=0 THEN ? ELSE delivered_at END, claimed_turn=CASE WHEN claimed_turn=0 THEN ? ELSE claimed_turn END, updated_at=?" +
		" WHERE room_id=? AND post_id=? AND session_id IN (" + inClause(set.Keys) + ") AND state NOT IN ('cancelled','dismissed') AND (? = 'woke' OR how <> 'woke')"
	for _, postID := range postIDs {
		args := []any{string(how), deliveredAt, turn, version, set.RoomID, postID}
		args = keyArgs(args, set.Keys)
		args = append(args, string(how))
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

// DeliveryWrite runs fn in one transaction on the delivery rows and publishes
// ev after it commits. A caller that must commit something else in the same
// transaction uses it: subagent's input claim keeps the delivered frames in
// its request receipt, so the claim and that receipt land together.
func (s *Store) DeliveryWrite(ctx context.Context, ev hub.Event, fn func(conn *sql.Conn) error) error {
	db, err := s.postIndexDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, ev, fn)
}

// deliveryWriteTime is the updated_at of every row one write changes: later
// than the newest row in the table, so each write moves the newest value
// forward and a signature made from it changes. Milliseconds alone can tie two
// writes, and members and the app read that value as a version.
func deliveryWriteTime(ctx context.Context, conn *sql.Conn) (int64, error) {
	var newest sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT MAX(updated_at) FROM post_deliveries").Scan(&newest); err != nil {
		return 0, err
	}
	if now := time.Now().UnixMilli(); now > newest.Int64 {
		return now, nil
	}
	return newest.Int64 + 1, nil
}

// execCount runs one statement and returns how many rows it changed.
func execCount(ctx context.Context, conn *sql.Conn, query string, args ...any) (int64, error) {
	result, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delivery state write: %w", err)
	}
	return result.RowsAffected()
}
