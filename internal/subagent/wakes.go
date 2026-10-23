package subagent

import (
	"context"
	"errors"
	"fmt"

	"dyna-agent/internal/room"
)

// TickSessions delivers stored input to every session and returns the
// sessions it started a turn for. Only the engine calls this pass in
// production; posting processes only append messages and their wake intent.
func (m *Manager) TickSessions(ctx context.Context) ([]string, error) {
	return m.tickSessions(ctx, nil)
}

// TickSessionTargets delivers only the recipients selected by new post events.
func (m *Manager) TickSessionTargets(ctx context.Context, targets []string) error {
	if len(targets) == 0 {
		return nil
	}
	_, err := m.tickSessions(ctx, targets)
	return err
}

func (m *Manager) tickSessions(ctx context.Context, targets []string) ([]string, error) {
	var sessions []Meta
	if targets == nil {
		var err error
		sessions, err = m.store().listMeta(false, true)
		if err != nil {
			return nil, err
		}
	} else {
		for _, ref := range targets {
			meta, err := m.store().deliveryMeta(ref)
			if errors.Is(err, ErrNotFound) {
				continue // The recipient was removed after the event.
			}
			if err != nil {
				return nil, err
			}
			sessions = append(sessions, meta)
		}
	}
	return m.tickSessionMetas(ctx, sessions)
}

// TickSessionRoom recovers only the room whose pause was lifted.
func (m *Manager) TickSessionRoom(ctx context.Context, roomID string) error {
	sessions, err := m.store().roomMeta(roomID, false)
	if err != nil {
		return err
	}
	_, err = m.tickSessionMetas(ctx, sessions)
	return err
}

func (m *Manager) tickSessionMetas(ctx context.Context, sessions []Meta) ([]string, error) {
	var failures []error
	seen := map[string]bool{}
	var started []string
	for _, meta := range sessions {
		if err := ctx.Err(); err != nil {
			return started, err
		}
		// A record published by Start before it allocates turn 1 is still being
		// created: a post must not allocate that turn, because the initial
		// brief and schema belong to it.
		if seen[meta.ID] || !meta.Closed.IsZero() || meta.ConsumedBy != "" || !meta.Resumable || meta.Turns == 0 || meta.Transfer.blocksTurns() || m.RoomPaused(meta.RoomID) {
			continue
		}
		seen[meta.ID] = true
		began, err := m.deliverSessionPosts(ctx, meta)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", meta.ID, err))
		}
		if began {
			started = append(started, meta.ID)
		}
	}
	return started, errors.Join(failures...)
}

// deliverSessionPosts wakes meta's session for its pending input and reports
// whether that started a turn. Input steered into a turn in flight is not
// counted: that turn already has the session's attention.
func (m *Manager) deliverSessionPosts(ctx context.Context, meta Meta) (bool, error) {
	posts, err := m.pendingWakePosts(meta)
	if err != nil {
		return false, err
	}
	if len(posts) > 0 {
		if m.checkpoint != nil {
			m.checkpoint("wake-selected")
		}
		res, err := m.Wake(ctx, meta.ID, WakeOptions{MessageID: posts[0].ID})
		if errors.Is(err, ErrBusy) {
			return false, nil // The completion event retries this still-pending input.
		}
		return err == nil && !res.Steered && !res.Paused, err
	}
	if meta.WrapUpAfter < meta.Turns && meta.Status != StatusDormant {
		m.wrapUp(ctx, meta.ID, meta.Turns)
	}
	return false, nil
}

// pendingWakePosts is shared by engine delivery and room-resume recovery.
// A resume stays pending only for input the engine can still admit.
func (m *Manager) pendingWakePosts(meta Meta, previous ...Turn) ([]room.Message, error) {
	// The keeper reads replies with its next assignment; a reply never wakes it.
	// A session with no harness session still takes input: its next turn opens
	// a fresh conversation.
	if !meta.Resumable || meta.Role == RoleKeeper {
		return nil, nil
	}
	posts, _, err := m.drainRoomMessages(meta, Turn{N: meta.Turns + 1})
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, nil
	}
	var last Turn
	if len(previous) > 0 {
		last = previous[0]
	} else {
		last, err = m.store().ReadTurn(meta.ID, meta.Turns)
		if err != nil {
			return nil, err
		}
	}
	var fresh map[string]bool
	if last.Status == TurnError || last.Status == TurnStopped {
		if last.InputCutoff != nil {
			newPosts, _, err := m.rooms().ReadAll(meta.RoomID, *last.InputCutoff)
			if err != nil {
				return nil, err
			}
			fresh = map[string]bool{}
			for _, post := range newPosts {
				fresh[post.ID] = true
			}
		} else {
			return nil, fmt.Errorf("terminal turn %s/%d has no input cutoff", meta.ID, last.N)
		}
	}
	selected := posts[:0]
	for _, post := range posts {
		if meta.Consolidating != "" && !handoffPost(meta, post.ID) {
			continue
		}
		if post.Wake && (fresh == nil || fresh[post.ID]) {
			selected = append(selected, post)
		}
	}
	return selected, nil
}

func handoffPost(meta Meta, postID string) bool {
	return meta.Transfer != nil && meta.Transfer.Phase == "waiting" && meta.Transfer.PostID == postID
}
