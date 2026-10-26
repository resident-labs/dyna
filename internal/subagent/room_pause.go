package subagent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"dyna-agent/internal/admission"
	"dyna-agent/internal/room"
)

var errRoomPaused = errors.New("room sessions are paused; posts remain stored")

func (m *Manager) RoomPaused(ref string) bool {
	paused, err := m.rooms().Paused(ref)
	return err != nil || paused
}
func (m *Manager) waitRoom(ctx context.Context, id string) error {
	for {
		paused, err := m.rooms().Paused(id)
		if err != nil {
			return err
		}
		if !paused {
			return nil
		}
		if err = m.transferSleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

// waitSession records the exact pending prompt before a room hold, so a
// crashed held runner can resume the same turn and native identity.
func (w *runnerWrites) waitSession(ctx context.Context, prompt string) error {
	m := w.m
	meta, err := m.store().ReadMeta(w.id)
	if err != nil {
		return err
	}
	held := false
	if prompt != "" {
		if err := w.noteWait(ctx, TurnNote{PendingPrompt: prompt}); err != nil {
			return err
		}
	}
	for {
		paused, err := m.rooms().Paused(meta.RoomID)
		if err != nil {
			return err
		}
		if !paused {
			// A turn that waited gives its machine slot back while held, and
			// takes one again now.
			if held && w.resume != nil {
				return w.resume(ctx)
			}
			return nil
		}
		if !held {
			if err := w.noteWait(ctx, TurnNote{RoomHeld: true}); err != nil {
				return err
			}
			held = true
			if w.suspend != nil {
				w.suspend()
			}
		}
		if err = m.transferSleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

// coordinatorPauseBudget holds outside provider work and returns the durable
// pause time to add to operation deadlines. Replays apply only the delta.
func (m *Manager) coordinatorPauseBudget(ctx context.Context, roomID string, since time.Time) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	paused, err := m.rooms().Paused(roomID)
	if err != nil {
		return 0, err
	}
	if paused {
		return 0, errRoomPaused
	}
	spans, err := m.rooms().PauseSpans(roomID, since)
	if err != nil {
		return 0, err
	}
	var total time.Duration
	for _, span := range spans {
		if span.To.IsZero() {
			return 0, errRoomPaused
		}
		total += span.To.Sub(span.From)
	}
	return total, nil
}

// roomExecutionGate serializes the short actual publication/launch boundary
// with pause. It never invokes a session mutation while holding the room lock.
func (m *Manager) roomExecutionGate(ctx context.Context, id string, execute func() error) error {
	return m.rooms().WithRoomSnapshot(ctx, id, func(rec room.Record) error {
		if rec.SessionPause.Paused {
			return errRoomPaused
		}
		return execute()
	})
}

// turnSlot is the machine slot of one running turn. A turn gives it back while
// its room is held, so a paused room never keeps another room from running.
type turnSlot struct {
	m     *Manager
	meta  Meta
	owner bool
	// held tells the session record that the limit holds the turn.
	held func(admission.Hold)
	mu   sync.Mutex
	free func()
}

// take waits for a slot unless the turn already holds one.
func (s *turnSlot) take(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.free != nil {
		return nil
	}
	free, err := s.m.admit(ctx, AdmitRequest{Meta: s.meta, Owner: s.owner, OnHold: s.held})
	if err != nil {
		return err
	}
	s.free = free
	return nil
}

// drop gives the slot back; calling it with none held does nothing.
func (s *turnSlot) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.free != nil {
		s.free()
		s.free = nil
	}
}

// ownerCaused reports whether the owner's own message started this turn: the
// owner sent the turn's first prompt, or the room's owner address is among the
// senders the turn owes a report to.
func ownerCaused(meta Meta, turn Turn) bool {
	return slices.Contains(turn.ReportTo, meta.Room) || turn.N == 1 && meta.Owner != ""
}
