package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
)

// DormantCloseAfter is how long a session can stay dormant before the sweep
// closes it.
const DormantCloseAfter = 7 * 24 * time.Hour

// Neutral outcomes of a dormant close. A session that gained work or still
// answers for work stays open by design, so the room controls show these as a
// kept session, not as an error.
var (
	errDormantGainedWork       = errors.New("session is no longer dormant")
	errDormantResponsibilities = errors.New("the session still has open responsibilities")
)

// keptForWork marks a dormant close error as the neutral "gained work"
// outcome, and keeps the detail text for logs.
func keptForWork(reason string) error {
	return fmt.Errorf("%w: %s", errDormantGainedWork, reason)
}

// keptReason maps a dormant close error to what the room controls show. Only
// RoomSessionKeptFailed is a real failure there.
func keptReason(err error) RoomSessionKeptReason {
	switch {
	case errors.Is(err, errDormantGainedWork):
		return RoomSessionKeptStartedWork
	case errors.Is(err, errDormantResponsibilities):
		return RoomSessionKeptResponsibilities
	default:
		return RoomSessionKeptFailed
	}
}

// keptRow is one session the dormant close left open.
func keptRow(meta Meta, err error) RoomSessionResult {
	reason := keptReason(err)
	return RoomSessionResult{Session: meta.ID, Name: meta.Address(), Status: "kept", Reason: reason, Error: err.Error(), OK: reason != RoomSessionKeptFailed}
}

// SweepDormantSessions is the automatic Clear dormant: it closes every session
// that has stayed dormant, with no activity, for DormantCloseAfter.
func (m *Manager) SweepDormantSessions(ctx context.Context) error {
	all, err := m.store().ListOpenMeta()
	if err != nil {
		return err
	}
	rooms := map[string]bool{}
	for _, meta := range all {
		if meta.Status == StatusDormant && meta.RoomID != "" {
			rooms[meta.RoomID] = true
		}
	}
	cutoff := m.store().now().Add(-DormantCloseAfter)
	var errs []error
	for id := range rooms {
		if _, err := m.closeDormantSessions(ctx, id, cutoff); err != nil {
			errs = append(errs, fmt.Errorf("room %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// ClearDormantSessions closes every dormant session in the room for the owner or root.
func (m *Manager) ClearDormantSessions(ctx context.Context, ref string) (RoomSessionMutation, error) {
	if err := authority.Check("room clear-dormant", authority.Of(ctx), nil); err != nil {
		return RoomSessionMutation{Results: []RoomSessionResult{}}, err
	}
	return m.closeDormantSessions(ctx, ref, m.store().now())
}

// closeDormantSessions closes the room's sessions dormant since cutoff through
// the safe close path. Each kept session reports why.
func (m *Manager) closeDormantSessions(ctx context.Context, ref string, cutoff time.Time) (RoomSessionMutation, error) {
	out := RoomSessionMutation{Results: []RoomSessionResult{}}
	state, err := m.RoomSessionControls(ref)
	if err != nil {
		return out, err
	}
	members, err := m.roomSessions(state.RoomID)
	if err != nil {
		return out, err
	}
	for _, meta := range members {
		if !dormantBefore(meta, cutoff) || m.store().lockHeld(meta.ID) {
			continue
		}
		row := RoomSessionResult{Session: meta.ID, Name: meta.Address(), Status: "closed", OK: true}
		if err = m.closeDormant(ctx, meta.ID, cutoff); err != nil {
			row = keptRow(meta, err)
			if row.OK {
				out.Kept++
			} else {
				out.Failed++
			}
		} else {
			out.Succeeded++
		}
		out.Results = append(out.Results, row)
	}
	out.State, err = m.RoomSessionControls(state.RoomID)
	return out, err
}

// DismissSession closes one dormant session for the owner or root.
func (m *Manager) DismissSession(ctx context.Context, ref string) (Info, error) {
	if err := authority.Check("DismissSession", authority.Of(ctx), nil); err != nil {
		return Info{}, err
	}
	info, err := m.store().Resolve(ref)
	if err != nil {
		return info, err
	}
	if err = m.closeDormant(ctx, info.ID, m.store().now()); err != nil {
		return info, err
	}
	return m.store().Info(info.ID)
}

// dormantBefore reports whether the session finished and stayed inactive
// since before cutoff. The sweep needs both: a session that finished today
// after a long idle turn is not yet long dormant.
func dormantBefore(meta Meta, cutoff time.Time) bool {
	return meta.Status == StatusDormant && !meta.Finished.IsZero() && meta.Finished.Before(cutoff) && !meta.LastActivity.IsZero() && meta.LastActivity.Before(cutoff)
}

// closeDormant reserves one dormant session, checks its Responsibilities
// again, and commits the close under the turn lock only while it is still
// dormant. A wake or new work that arrives first keeps the session open.
func (m *Manager) closeDormant(ctx context.Context, id string, cutoff time.Time) error {
	token := NewID()
	defer m.releaseDormant(id, token)
	if err := m.reserveDormant(ctx, id, token, cutoff); err != nil {
		return err
	}
	if m.checkpoint != nil {
		m.checkpoint("dormant-reserved")
	}
	if err := m.checkDormantOwnership(ctx, id); err != nil {
		return err
	}
	_, err := m.CloseWithOptions(ctx, id, CloseOptions{DormantBefore: cutoff, closingToken: token})
	return err
}

// closeDormantLocked commits through the same close path after the names lock.
// Metadata -> turn -> gate -> room makes post append, child creation and turn
// startup serialize with the final evidence check. It never stops a live turn.
func (m *Manager) closeDormantLocked(ctx context.Context, id string, cutoff time.Time, token string) error {
	store := m.store()
	unlock, err := store.lockSessionContext(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	turn, err := store.tryTurnLock(id)
	if err != nil {
		return keptForWork("the session started a turn")
	}
	defer turn.Close()
	return store.withGateLockContext(ctx, id, func(dir string) error {
		meta, err := store.ReadMeta(id)
		if err != nil {
			return err
		}
		return m.rooms().WithRoomSnapshot(ctx, meta.RoomID, func(_ room.Record) error {
			if token == "" || meta.Closing == nil || meta.Closing.Token != token {
				return keptForWork("the close reservation changed")
			}
			if !meta.Closing.Active() {
				return keptForWork("the close check outlasted its reservation")
			}
			if !meta.Closed.IsZero() {
				return keptForWork("another action already closed the session")
			}
			if !dormantBefore(meta, cutoff) {
				return errDormantGainedWork
			}
			if meta.Consolidating != "" || meta.Transfer.Active() {
				return keptForWork("the session has a transfer or consolidation reservation")
			}
			if meta.Turns > 0 {
				t, e := store.ReadTurn(id, meta.Turns)
				if e != nil {
					return e
				}
				if !t.Done() || t.RoomHeld || t.HeldPrompt != "" {
					return keptForWork("the session has an unfinished turn")
				}
			}
			pending, e := m.pendingRoomMessages(meta)
			if e != nil {
				return e
			}
			if len(pending) > 0 {
				return fmt.Errorf("%w: %w", errDormantGainedWork, pendingMessagesError(meta, pending))
			}
			members, e := m.roomSessions(meta.RoomID)
			if e != nil {
				return e
			}
			for _, child := range members {
				if child.ParentSession == id && child.Status != StatusDormant {
					return keptForWork("the session has an unfinished child")
				}
			}
			if m.checkpoint != nil {
				m.checkpoint("dormant-close-write")
			}
			meta.Closed, meta.Closing = store.now(), nil
			// The closing mark can expire while the close checks, and work
			// that wakes the session takes no session lock (ActivateTx), so the
			// write commits only if the stored record is still this reserved,
			// dormant session.
			return store.writeMetaIf(context.Background(), meta, func(cur Meta) error {
				if cur.Closing == nil || cur.Closing.Token != token || !cur.Closed.IsZero() || !dormantBefore(cur, cutoff) {
					return keptForWork("the session changed during the dormant close")
				}
				return nil
			})
		})
	})
}

// unpublishedDormantJob is called with this store's publication lock held.
// Only staging or orphan artifacts without execution evidence are ignorable.
// A present meta.json (even a broken symlink) is always published evidence.
func unpublishedDormantJob(root, id string) (bool, error) {
	dir := filepath.Join(root, id)
	if _, err := os.Lstat(filepath.Join(dir, "meta.json")); !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
		switch entry.Name() {
		case "script.js", "plan.json", "launch.json", "daemon.log", "result.json":
			// Staging or orphan artifacts alone cannot execute. Create
			// opens both execution journals before publishing metadata.
			// Even empty journals therefore remain ambiguous evidence.
		default:
			return false, nil
		}
	}
	return true, nil
}

func (m *Manager) reserveDormant(ctx context.Context, id, token string, cutoff time.Time) error {
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
		if meta.Closing.Active() {
			return keptForWork("the session has another close check")
		}
		if !meta.Closed.IsZero() || !dormantBefore(meta, cutoff) || m.store().lockHeld(id) {
			return errDormantGainedWork
		}
		if meta.Consolidating != "" || meta.Transfer.Active() {
			return keptForWork("the session has a reservation")
		}
		meta.Closing = &sessionlife.Closing{Token: token, PID: os.Getpid(), At: time.Now()}
		return m.store().writeMeta(meta)
	})
}
func (m *Manager) releaseDormant(id, token string) {
	_ = m.store().withLock(id, func() error {
		meta, err := m.store().ReadMeta(id)
		if err != nil {
			return err
		}
		if meta.Closing != nil && meta.Closing.Token == token {
			meta.Closing = nil
			return m.store().writeMeta(meta)
		}
		return nil
	})
}
func (m *Manager) checkDormantOwnership(ctx context.Context, id string) error {
	meta, err := m.store().ReadMeta(id)
	if err != nil {
		return err
	}
	items, err := m.Responsibilities(ctx, Info{Meta: meta, Status: meta.Status})
	if err != nil {
		return err
	}
	if len(items) > 0 {
		return errors.Join(errDormantResponsibilities, responsibilityError("close a dormant session", items))
	}
	return nil
}
