package subagent

import (
	"context"
	"errors"
	"fmt"

	"dyna-agent/internal/authority"
)

// DismissPendingPost is an explicit owner or root action. It preserves the room
// log and records a target receipt that prevents delivery from every origin.
// The turn lock makes it impossible to recall input from a running harness.
func (m *Manager) DismissPendingPost(ctx context.Context, ref, messageID string, confirmed bool) error {
	if err := authority.Check("subagent dismiss-pending", authority.Of(ctx), nil); err != nil {
		return err
	}
	if !confirmed {
		return errors.New("dismissal requires explicit confirmation; history stays, but the session will not receive this post")
	}
	store := m.store()
	info, err := store.resolve(ref, false)
	if err != nil {
		return err
	}
	return store.withLockContext(ctx, info.ID, func() error {
		turn, err := store.tryTurnLock(info.ID)
		if err != nil {
			return fmt.Errorf("cannot dismiss a post during active delivery: %w", err)
		}
		defer turn.Close()
		return store.withGateLockContext(ctx, info.ID, func(dir string) error {
			meta, err := store.ReadMeta(info.ID)
			if err != nil {
				return err
			}
			if !meta.Closed.IsZero() || meta.Consolidating != "" || meta.Transfer.Active() {
				return errors.New("session is closed or reserved")
			}
			if meta.Turns > 0 {
				latest, err := store.ReadTurn(meta.ID, meta.Turns)
				if err != nil {
					return err
				}
				if !latest.Done() || latest.RoomHeld || latest.HeldPrompt != "" {
					return errors.New("cannot dismiss a post while an unfinished or held turn may own its input")
				}
			}
			q, err := readQueueForUpdate(dir)
			if err != nil {
				return err
			}
			if q.Dismissed[messageID] {
				return nil
			}
			pending, err := m.pendingRoomMessages(meta)
			if err != nil {
				return err
			}
			for _, msg := range pending {
				if msg.ID == messageID {
					q.Dismissed[messageID] = true
					return writeQueue(dir, q)
				}
			}
			return fmt.Errorf("message %s is not pending for session %s", messageID, meta.ID)
		})
	})
}
