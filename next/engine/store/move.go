package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

var ErrTreeForbidden = errors.New("tree access forbidden")

// MoveSession commits the tree change and event in one transaction.
func (s *Store) MoveSession(ctx context.Context, target, parent contract.SessionID, event contract.Event, maxDepth int) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		tree, err := loadTree(ctx, tx, event.Room)
		if err != nil {
			return err
		}
		caller := event.From
		if !tree.CanManage(caller, target) || !(tree.CanManage(caller, parent) || caller.Kind == contract.PrincipalSession && caller.Session == parent) {
			return ErrTreeForbidden
		}
		if err := tree.CheckMove(target, parent); err != nil {
			return &TreeError{Message: err.Error()}
		}
		order, err := tree.CloseOrder(target)
		if err != nil {
			return &TreeError{Message: err.Error()}
		}
		if depth := tree.Depth(parent) + 1 + tree.Depth(order[0]) - tree.Depth(target); maxDepth > 0 && depth > maxDepth {
			return &TreeError{Message: fmt.Sprintf("the move would put the deepest session of this subtree at depth %d, past the room's limit of %d; choose a parent higher in the tree", depth, maxDepth)}
		}
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=? AND closed_at IS NULL", parent).Scan(&state); err != nil {
			return err
		}
		if state == "closing" {
			return ErrParentClosing
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET parent=? WHERE id=?", parent, target); err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}

// SetRecreated records the new image after the container was created. A notice tells the session, and wakes it when it holds an
// open goal.
func (s *Store) SetRecreated(ctx context.Context, id contract.BoxID, image string, notice *contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE boxes SET image=? WHERE id=?", image, id); err != nil {
			return err
		}
		if notice == nil {
			return nil
		}
		return s.restartNotice(ctx, tx, *notice)
	})
}
