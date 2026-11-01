package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

// sqlQuery matches database/sql's variadic query API, which requires any for SQL arguments.
type sqlQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// RoomTree loads open sessions in a room for the pure tree rules.
func (s *Store) RoomTree(ctx context.Context, id contract.RoomID) (room.Tree, error) {
	return loadTree(ctx, s.Read(), id)
}

// ClosingParent finds a marked parent after its last open child closes.
func (s *Store) ClosingParent(ctx context.Context, child contract.SessionID) (contract.SessionID, error) {
	var parent contract.SessionID
	err := s.Read().QueryRowContext(ctx, "SELECT p.id FROM sessions c JOIN sessions p ON p.id=c.parent WHERE c.id=? AND p.state='closing' AND p.closed_at IS NULL AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.parent=p.id AND s.closed_at IS NULL)", child).Scan(&parent)
	return parent, err
}

func (s *Store) ClosingLeaf(ctx context.Context, id contract.SessionID) (bool, error) {
	var leaf bool
	err := s.Read().QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sessions s WHERE s.id=? AND s.state='closing' AND s.closed_at IS NULL AND NOT EXISTS (SELECT 1 FROM sessions c WHERE c.parent=s.id AND c.closed_at IS NULL))", id).Scan(&leaf)
	return leaf, err
}

// MarkClosing blocks child starts across the subtree in one write.
func (s *Store) MarkClosing(ctx context.Context, target contract.SessionID, discard bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `WITH RECURSIVE sub(id) AS (
			SELECT ? UNION SELECT s.id FROM sessions s JOIN sub ON s.parent=sub.id WHERE s.closed_at IS NULL
		) UPDATE sessions SET state='closing' WHERE id IN (SELECT id FROM sub) AND closed_at IS NULL`, target)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hostexec_runs SET stopped='closed' WHERE ended_at IS NULL AND grant_id IN (SELECT g.id FROM grants g JOIN sessions s ON s.id=g.to_id WHERE g.to_kind='session' AND s.state='closing')`); err != nil {
			return err
		}
		want := WantClose
		if discard {
			want = WantDiscard
		}
		_, err = tx.ExecContext(ctx, `WITH RECURSIVE sub(id) AS (
			SELECT ? UNION SELECT s.id FROM sessions s JOIN sub ON s.parent=sub.id WHERE s.closed_at IS NULL
		) UPDATE boxes SET want=?,due_at=0 WHERE session IN (SELECT id FROM sub) AND want!='discard'`, target, want)
		return err
	})
}

// TreeError reports a tree rule refusal from the write transaction.
type TreeError struct{ Message string }

func (e *TreeError) Error() string { return e.Message }

func loadTree(ctx context.Context, q sqlQuery, id contract.RoomID) (room.Tree, error) {
	rows, err := q.QueryContext(ctx, "SELECT s.id,coalesce(s.parent,''),coalesce(b.host,'') FROM sessions s LEFT JOIN boxes b ON b.session=s.id WHERE s.room=? AND s.closed_at IS NULL ORDER BY s.created_at,s.id", id)
	if err != nil {
		return room.Tree{}, err
	}
	defer rows.Close()
	var nodes []room.Node
	for rows.Next() {
		var node room.Node
		if err := rows.Scan(&node.ID, &node.Parent, &node.Host); err != nil {
			return room.Tree{}, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return room.Tree{}, err
	}
	return room.NewTree(nodes), nil
}

func checkDepth(depth, limit int) error {
	if limit > 0 && depth > limit {
		return &TreeError{Message: fmt.Sprintf("session depth %d exceeds limit %d", depth, limit)}
	}
	return nil
}
