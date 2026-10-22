package subagent

import (
	"context"
	"errors"
	"os"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
)

// Finish makes a session dormant. It works during the session's own turn.
// It refuses while the session owns any responsibility. A dormant session
// runs nothing; a coordination post wakes it. Notices do not wake it.
func (m *Manager) Finish(ctx context.Context, ref string) (Info, error) {
	if err := authority.Check("subagent finish", authority.Of(ctx), nil); err != nil {
		return Info{}, err
	}
	return m.finish(ctx, ref, 0)
}

// afterTurn uses the same reservation, responsibility check and transition for
// automatic return. Zero is an explicit finish, which may run mid-turn.
func (m *Manager) finish(ctx context.Context, ref string, afterTurn int) (Info, error) {
	info, err := m.store().Resolve(ref)
	if err != nil {
		return Info{}, err
	}
	if info.Status == StatusClosed {
		return info, ErrClosed
	}
	if afterTurn > 0 && (!info.DormantWake || info.Meta.Status == StatusDormant || info.Turns != afterTurn) {
		return info, nil
	}
	// Reserve the session first. Goal, question, PR, shell and workflow
	// assignment and child creation all refuse a reserved session, so nothing
	// new can arrive between the responsibility check and the commit.
	token := NewID()
	if err = m.reserveFinish(ctx, info.ID, token); err != nil {
		return info, err
	}
	defer m.releaseDormant(info.ID, token)
	items, err := m.Responsibilities(ctx, info)
	if err != nil {
		return info, err
	}
	if len(items) > 0 {
		if afterTurn > 0 {
			// Taking work consumes the temporary wake. Later idle turns keep
			// the ordinary explicit-finish contract.
			_, err := m.store().updateMetaContext(ctx, info.ID, func(meta *Meta) {
				if meta.Turns == afterTurn {
					meta.DormantWake = false
				}
			})
			return info, err
		}
		return info, responsibilityError("finish", items)
	}
	if m.checkpoint != nil {
		m.checkpoint("finish-checked")
	}
	committed := false
	err = m.store().withLockContext(ctx, info.ID, func() error {
		meta, err := m.store().ReadMeta(info.ID)
		if err != nil {
			return err
		}
		if !meta.Closed.IsZero() {
			return ErrClosed
		}
		if meta.Closing == nil || meta.Closing.Token != token {
			return errors.New("finish reservation changed")
		}
		// An expired reservation no longer fenced off new work, so its check
		// cannot vouch for the session now.
		if !meta.Closing.Active() {
			return errors.New("finish check outlasted its reservation; run finish again")
		}
		commit := func() error {
			meta.Status, meta.Finished, meta.Closing, meta.DormantWake = StatusDormant, time.Now(), nil, false
			if err := m.store().writeMeta(meta); err != nil {
				return err
			}
			committed = true
			return nil
		}
		if afterTurn == 0 {
			return commit()
		}
		if !meta.DormantWake || meta.Turns != afterTurn || meta.Transfer.blocksTurns() || meta.Consolidating != "" {
			return nil
		}
		turn, err := m.store().ReadTurn(info.ID, afterTurn)
		if err != nil {
			return err
		}
		if !turn.Done() || turn.RoomHeld || turn.HeldPrompt != "" {
			return nil
		}
		// Fence both queue claims and appended posts through the commit.
		return m.store().withGateLockContext(ctx, info.ID, func(string) error {
			return m.rooms().WithRoomSnapshot(ctx, meta.RoomID, func(room.Record) error {
				pending, err := m.pendingRoomMessages(meta)
				if err != nil {
					return err
				}
				if len(pending) > 0 {
					return nil
				}
				return commit()
			})
		})
	})
	if err != nil {
		return info, err
	}
	if committed {
		m.store().appendEvent(info.ID, map[string]any{"t": "session_finished"})
	}
	return m.store().Info(info.ID)
}

// reserveFinish marks the session as checked, under the names lock so a child
// start in progress completes first. It works during the session's own turn.
func (m *Manager) reserveFinish(ctx context.Context, id, token string) error {
	unlock, err := lockNamesContext(ctx, m.store())
	if err != nil {
		return err
	}
	defer unlock()
	return m.store().withLockContext(ctx, id, func() error {
		meta, err := m.store().ReadMeta(id)
		if err != nil {
			return err
		}
		if !meta.Closed.IsZero() {
			return ErrClosed
		}
		if meta.Closing.Active() {
			return errors.New("session has another close or finish check; retry after it")
		}
		meta.Closing = &sessionlife.Closing{Token: token, PID: os.Getpid(), At: time.Now()}
		return m.store().writeMeta(meta)
	})
}
