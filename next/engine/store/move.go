package store

import (
	"context"
	"database/sql"
	"errors"

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
		if err := checkDepth(tree.Depth(parent)+1+tree.Depth(order[0])-tree.Depth(target), maxDepth); err != nil {
			return err
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

// SetRecreated records the new slice and image after the container was created. A notice tells the session once, with
// its next turn.
func (s *Store) SetRecreated(ctx context.Context, id contract.BoxID, slice, image string, notice *contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE boxes SET slice=?, image=? WHERE id=?", slice, image, id); err != nil {
			return err
		}
		if notice == nil {
			return nil
		}
		seq, err := s.AppendEvent(tx, *notice)
		if err != nil {
			return err
		}
		to := contract.Principal{Kind: contract.PrincipalSession, Session: notice.Body.(contract.SessionRestarted).Session}
		return s.InsertDelivery(ctx, tx, seq, notice.Body, contract.Delivery{To: to, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: false})
	})
}
