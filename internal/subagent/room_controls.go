package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"dyna-agent/internal/authority"
)

// Existing names stay as aliases; the prefixed names let tygo emit a closed union.
const (
	RoomSessionKeptStartedWork      = RoomSessionKeptReasonStartedWork
	RoomSessionKeptResponsibilities = RoomSessionKeptReasonResponsibilities
	RoomSessionKeptFailed           = RoomSessionKeptReasonFailed
)

// roomSessions excludes unassigned history. Corrupt evidence cannot silently
// disappear from a destructive room action.
func (m *Manager) roomSessions(roomID string) ([]Meta, error) {
	return m.store().roomMeta(roomID, true)
}

func (m *Manager) RoomSessionControls(ref string) (RoomSessionState, error) {
	p, err := m.rooms().SessionPause(ref)
	if err != nil {
		return RoomSessionState{}, err
	}
	state := RoomSessionState{RoomID: p.RoomID, Paused: p.Paused, PausedAt: p.PausedAt, ResumedAt: p.ResumedAt}
	all, err := m.store().ListOpenMeta()
	if err != nil {
		return state, err
	}
	members := []Meta{}
	for _, meta := range all {
		if meta.RoomID == p.RoomID {
			members = append(members, meta)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Address() < members[j].Address() })
	for _, meta := range members {
		held := false
		if meta.Turns > 0 {
			t, e := m.store().ReadTurn(meta.ID, meta.Turns)
			if e != nil {
				return state, e
			}
			held = t.RoomHeld || t.HeldPrompt != "" && !t.HeldAccepted && !m.store().lockHeld(meta.ID)
			if !t.Done() && m.store().lockHeld(meta.ID) && !held {
				state.Running++
			}
		}
		pending, e := m.pendingRoomWork(meta)
		if e != nil {
			return state, e
		}
		if held || p.Paused && pending {
			state.Held++
		}
		if meta.Status == StatusDormant {
			state.Dormant++
		}
	}
	return state, nil
}
func (m *Manager) PauseRoomSessions(ctx context.Context, ref string) (RoomSessionMutation, error) {
	return m.setRoomSessions(ctx, ref, true)
}
func (m *Manager) ResumeRoomSessions(ctx context.Context, ref string) (RoomSessionMutation, error) {
	return m.setRoomSessions(ctx, ref, false)
}
func (m *Manager) setRoomSessions(ctx context.Context, ref string, paused bool) (RoomSessionMutation, error) {
	out := RoomSessionMutation{Results: []RoomSessionResult{}}
	action := "room pause"
	if !paused {
		action = "room resume"
	}
	if err := authority.Check(action, authority.Of(ctx), nil); err != nil {
		return out, err
	}
	p, err := m.rooms().SetSessionsPaused(ctx, ref, paused)
	if err != nil {
		return out, err
	}
	if !paused {
		if err = m.launchRoomResume(p.RoomID, p.Revision); err != nil {
			return out, fmt.Errorf("room resumed; recovery remains pending: %w", err)
		}
	}
	members, err := m.roomSessions(p.RoomID)
	if err != nil {
		return out, err
	}
	for _, meta := range members {
		if paused && meta.Turns > 0 {
			_, e := m.store().updateTurnContext(ctx, meta.ID, meta.Turns, func(t *Turn) {
				if t.Status == TurnQueued && !t.Done() {
					t.RoomHeld = true
				}
			})
			if e != nil {
				return out, e
			}
		}
		status := "paused"
		if !paused {
			status = "resumed"
		}
		if m.store().lockHeld(meta.ID) {
			status = "running"
			if t, e := m.store().ReadTurn(meta.ID, meta.Turns); e == nil && t.RoomHeld {
				status = "held"
			}
		}
		out.Results = append(out.Results, RoomSessionResult{Session: meta.ID, Name: meta.Address(), OK: true, Status: status})
		out.Succeeded++
	}
	out.State, err = m.RoomSessionControls(p.RoomID)
	return out, err
}
func (m *Manager) launchRoomResume(id string, revision uint64) error {
	if m.ResumeRoomLaunch != nil {
		return m.ResumeRoomLaunch(m, id, revision)
	}
	m.workers().Go(fmt.Sprintf("room-resume %s %d", id, revision), func(ctx context.Context) error {
		return m.runRoomResume(ctx, id, revision)
	})
	return nil
}

// runRoomResume is replayable. The revision prevents an old worker from
// admitting work after another pause. Cursors/claims coalesce repeated wakes.
func (m *Manager) runRoomResume(ctx context.Context, id string, revision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rec, err := m.rooms().Resolve(id)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(m.store().root(), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(m.store().root(), ".resume-"+rec.ID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	// An earlier release may still own this room's detached worker lock.
	for delay := 200 * time.Millisecond; ; delay = min(delay*2, time.Second) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if err := m.transferSleep(ctx, delay); err != nil {
			return err
		}
	}
	members, err := m.roomSessions(rec.ID)
	if err != nil {
		return err
	}
	for _, meta := range members {
		if err = ctx.Err(); err != nil {
			return err
		}
		p, e := m.rooms().SessionPause(rec.ID)
		if e != nil {
			return e
		}
		if p.Paused || p.Revision != revision {
			return nil
		}
		if meta.Consolidating != "" || meta.Transfer.blocksTurns() {
			continue
		}
		if meta.Turns > 0 {
			t, e := m.store().ReadTurn(meta.ID, meta.Turns)
			if e != nil {
				return e
			}
			if !t.Done() && (t.Status == TurnQueued || t.RoomHeld || t.HeldPrompt != "" && !t.HeldAccepted) {
				err = m.recoverHeldTurn(ctx, meta, t)
				if err != nil {
					return err
				}
				continue
			}
		}
	}
	// Keep the durable recovery bit until pending launch evidence is accepted.
	members, err = m.roomSessions(rec.ID)
	if err != nil {
		return err
	}
	for _, meta := range members {
		if meta.Turns == 0 {
			continue
		}
		t, e := m.store().ReadTurn(meta.ID, meta.Turns)
		if e != nil {
			return e
		}
		if !t.Done() && (t.Status == TurnQueued || t.RoomHeld || t.HeldPrompt != "" && !t.HeldAccepted) {
			return nil
		}
		pending, err := m.pendingWakePosts(meta, t)
		if err != nil {
			return err
		}
		// The engine has not admitted this input yet.
		if len(pending) > 0 {
			return nil
		}
	}
	return m.rooms().CompleteSessionResume(ctx, rec.ID, revision)
}
func (m *Manager) recoverHeldTurn(ctx context.Context, meta Meta, t Turn) error {
	unlock, err := m.store().lockSessionContext(ctx, meta.ID)
	if err != nil {
		return err
	}
	defer unlock()
	fresh, err := m.store().ReadMeta(meta.ID)
	if err != nil {
		return err
	}
	if fresh.Closing.Active() || !fresh.Closed.IsZero() || fresh.Consolidating != "" || fresh.Transfer.blocksTurns() || m.RoomPaused(fresh.RoomID) {
		return nil
	}
	lock, err := m.store().tryTurnLock(meta.ID)
	if errors.Is(err, ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := m.store().ReadTurn(meta.ID, t.N)
	if err != nil || current.Done() {
		lock.Close()
		return err
	}
	// Publish queued ownership before releasing metadata or spawning a runner.
	if err := m.store().prepareGate(meta.ID, t.N); err != nil {
		lock.Close()
		return err
	}
	spawner := m.Spawner
	if spawner == nil {
		spawner = ExecSpawner{}
	}
	if err = spawner.Spawn(m, meta.ID, t.N, lock); err != nil {
		lock.Close()
	}
	return err
}

// pendingRoomWork includes claimed-but-unacknowledged posts and every inherited
// origin.
func (m *Manager) pendingRoomWork(meta Meta) (bool, error) {
	pending, err := m.pendingRoomMessages(meta)
	return len(pending) > 0, err
}

// RecoverRoomSessionResumes restarts abandoned resume sweeps after app reopen.
// It starts no model itself, and each worker verifies the durable revision.
func (m *Manager) RecoverRoomSessionResumes(ctx context.Context) {
	for {
		records, err := m.rooms().List(true)
		if err == nil {
			for _, rec := range records {
				if rec.SessionPause.ResumePending && !rec.SessionPause.Paused {
					_ = m.launchRoomResume(rec.ID, rec.SessionPause.Revision)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
