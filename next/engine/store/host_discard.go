package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// HostRemovedReason is the reason on a box failed because the owner removed its host.
const HostRemovedReason = "host removed"

// DiscardHostBoxes closes a host to admission and fails every box on it, as a host lost for good, in one write. Only what is
// not a box can refuse it. A box being closed has no archive left to make, so its close discards it. It returns every box
// left on the host, each now failed; their volumes are the caller's to remove while the host still has its row.
func (s *Store) DiscardHostBoxes(ctx context.Context, host contract.HostName, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error)) ([]LifecycleBox, error) {
	var boxes []LifecycleBox
	err := s.Write(ctx, func(tx *sql.Tx) error {
		boxes = nil
		if err := hostHeld(ctx, tx, host, false); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE hosts SET state='draining' WHERE name=? AND state!='provisioning'", host)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("host %s: %w", host, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE boxes SET want='discard' WHERE host=? AND state!='deleted' AND want IN ('close','hold')", host); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT b.id,b.session,s.room,b.state,b.reason,b.slice,b.image,b.want FROM boxes b JOIN sessions s ON s.id=b.session
			WHERE b.host=? AND b.state!='deleted' ORDER BY b.id`, host)
		if err != nil {
			return err
		}
		for rows.Next() {
			b := LifecycleBox{Box: box.Box{Host: host}}
			if err := rows.Scan(&b.ID, &b.Session, &b.Room, &b.State, &b.Reason, &b.Slice, &b.Image, &b.Want); err != nil {
				rows.Close()
				return err
			}
			boxes = append(boxes, b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, b := range boxes {
			if b.State == contract.BoxFailed {
				continue
			}
			// The owner's removal fails a box from any state, so it is not one of box.Transition's lifecycle steps: a pending or
			// stopped box fails nowhere else.
			event, err := newEvent(b.Room, contract.BoxChanged{Box: b.ID, Session: b.Session, Host: host, From: b.State, To: contract.BoxFailed, Reason: HostRemovedReason, Cause: contract.CauseHostLost})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE boxes SET state='failed',reason=?,due_at=0,updated_at=? WHERE id=?", HostRemovedReason, event.Time.UnixMilli(), b.ID); err != nil {
				return err
			}
			if _, err := s.AppendEvent(tx, event); err != nil {
				return err
			}
			boxes[i].State, boxes[i].Reason, boxes[i].Updated = contract.BoxFailed, HostRemovedReason, event.Time
		}
		return s.failServices(ctx, tx, host, newEvent)
	})
	return boxes, err
}

// ReleaseHostBoxes lets go of a host for its failed and deleted boxes, so its removal or a node's retirement can go on. A
// failed box with no host starts on any host when its session is restarted.
func (s *Store) ReleaseHostBoxes(ctx context.Context, host contract.HostName) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET host=NULL WHERE host=? AND state IN ('failed','deleted')", host)
		return err
	})
}
