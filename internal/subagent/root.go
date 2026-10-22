package subagent

import (
	"context"
	"errors"
	"os"

	"dyna-agent/internal/room"
)

const rootGoal = "Orchestrate this room as its internal root while you hold the root seat; done when the owner closes this session."

// RootSession returns the room's open internal root session, if any.
func (m *Manager) RootSession(roomID string) (Info, bool, error) {
	meta, open, err := m.rootMeta(roomID)
	if err != nil || !open {
		return Info{}, false, err
	}
	root, err := m.store().Info(meta.ID)
	return root, err == nil, err
}

// rootMeta finds the root without repairing its turn or publishing completion.
func (m *Manager) rootMeta(roomID string) (Meta, bool, error) {
	sessions, err := m.store().roomMeta(roomID, false)
	if err != nil {
		return Meta{}, false, err
	}
	for _, info := range sessions {
		if info.Role == RoleRoot {
			return info, true, nil
		}
	}
	return Meta{}, false, nil
}

// StartRoot starts the room's internal root with input as its first turn. It
// works in the room's repo, or in the home directory for a room without one.
// A messageID selects stored input for the shared runner drain instead of input.
// When another caller started it first, StartRoot returns that session.
func (m *Manager) StartRoot(rec room.Record, input, messageID string) (Info, bool, error) {
	profs, err := m.profiles()
	if err != nil {
		return Info{}, false, err
	}
	name := ""
	if p, ok := profs.Get(m.settings().RootProfile); ok && !p.Disabled {
		name = p.Name
	}
	dir := rec.Repo
	if st, err := os.Stat(dir); dir == "" || err != nil || !st.IsDir() {
		if dir, err = os.UserHomeDir(); err != nil {
			return Info{}, false, err
		}
	}
	info, _, err := m.StartForEngine(StartOptions{Role: RoleRoot, Name: RoleRoot, Profile: name, Room: rec.ID, Dir: dir, Goal: rootGoal, Task: input, MessageID: messageID})
	if errors.Is(err, ErrNameTaken) {
		info, ok, e := m.RootSession(rec.ID)
		if e == nil && ok {
			return info, false, nil
		}
	}
	return info, err == nil, err
}

// ReturnRootSeat gives the room's root seat back to the internal root. In the
// same step, under the room lock, it moves the open internal root's room
// cursor to the log end: posts the external orchestrator handled never reach
// the internal root, and every later post does. It reports whether the holder
// changed and whether an internal root is open.
func (m *Manager) ReturnRootSeat(ctx context.Context, roomID string) (room.Seat, bool, bool, error) {
	info, open, err := m.RootSession(roomID)
	if err != nil {
		return room.Seat{}, false, false, err
	}
	if !open {
		seat, changed, err := m.rooms().SetSeat(ctx, roomID, room.Seat{}, nil)
		return seat, changed, false, err
	}
	var seat room.Seat
	var changed bool
	err = m.store().withLockContext(ctx, info.ID, func() error {
		var e error
		seat, changed, e = m.rooms().SetSeat(ctx, roomID, room.Seat{}, func(end int64) error {
			meta, err := m.store().ReadMeta(info.ID)
			if err != nil || end <= meta.RoomCursor {
				return err
			}
			meta.RoomCursor = end
			return m.store().writeMeta(meta)
		})
		return e
	})
	return seat, changed, true, err
}

// RootSeatHandoff uses the transfer coordinator to collect the internal root's
// handoff and stop its turn. It leaves the native conversation available for
// detach and never starts a replacement model.
func (m *Manager) RootSeatHandoff(ctx context.Context, roomID string) (string, error) {
	info, open, err := m.RootSession(roomID)
	if err != nil || !open {
		return "", err
	}
	state, err := m.TransferContext(ctx, info.ID, TransferOptions{
		Profile: info.Profile, Model: info.Model, WaitSeconds: 30, Fresh: true, rootSeat: true,
	})
	if err != nil {
		return "", err
	}
	if err := m.runTransfer(ctx, info.ID, state.ID); err != nil {
		return "", err
	}
	stateNow, err := m.TransferStatus(info.ID)
	if err != nil {
		return "", err
	}
	if stateNow == nil || stateNow.ID != state.ID || stateNow.Phase != "completed" {
		return "", errors.New("root handoff did not complete")
	}
	return stateNow.Handoff, nil
}
