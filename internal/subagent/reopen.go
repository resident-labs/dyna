package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
)

// Reopen restores a closed conversation without starting a provider process.
// An address selects its most recently closed history; an ID is exact.
func (m *Manager) Reopen(ctx context.Context, ref string) (Info, error) {
	if err := authority.Check("subagent reopen", authority.Of(ctx), nil); err != nil {
		return Info{}, err
	}
	store := m.store()
	unlock, err := lockNamesContext(ctx, store)
	if err != nil {
		return Info{}, err
	}
	defer unlock()
	all, err := store.list(true, false)
	if err != nil {
		return Info{}, err
	}
	selected, err := store.ResolveClosed(ref)
	if err != nil {
		return Info{}, err
	}
	err = store.withLock(selected.ID, func() error {
		meta, err := store.ReadMeta(selected.ID)
		if err != nil {
			return err
		}
		if meta.Closed.IsZero() {
			return errors.New("session is already open")
		}
		if meta.ConsumedBy != "" {
			return fmt.Errorf("session was consolidated into %s; reopen that session instead", meta.ConsumedBy)
		}
		if !meta.Resumable || meta.HarnessSession == "" {
			return errors.New("session is not resumable: no resumable harness conversation is available")
		}
		if store.lockHeld(meta.ID) {
			return errors.New("session still has an active turn; wait for it to stop before reopening")
		}
		if meta.Consolidating != "" || meta.Transfer.blocksTurns() {
			return errors.New("session has an unfinished consolidation or model transfer")
		}
		for _, other := range all {
			if other.ID != meta.ID && other.RoomID == meta.RoomID && other.EffectiveName() == meta.EffectiveName() && other.Closed.IsZero() {
				return fmt.Errorf("cannot reopen %s: its name is used by session %s", meta.Address(), other.ID)
			}
		}
		// Keep unread posts from this session's lifetime. Exclude only posts
		// from the closed interval, which can belong to a reused address: those
		// named this address without binding a session to it, so their row
		// carries the address and no session ID.
		messages, _, err := m.rooms().ReadAll(meta.RoomID, meta.RoomCursor)
		if err != nil {
			return err
		}
		byAddress, err := inputRows(m.rooms(), meta.RoomID, "", meta.Address(), postIDs(messages))
		if err != nil {
			return err
		}
		for _, msg := range messages {
			if _, namedAddress := byAddress[msg.ID]; namedAddress && msg.TS > meta.Closed.UnixMilli() {
				if meta.ClosedPosts == nil {
					meta.ClosedPosts = map[string]bool{}
				}
				meta.ClosedPosts[msg.ID] = true
			}
		}
		meta.Closed = time.Time{}
		meta.Closing = nil
		meta.Status, meta.Finished = StatusIdle, time.Time{}
		meta.LastActivity = time.Now()
		if err := store.writeMeta(meta); err != nil {
			return err
		}
		store.appendEvent(meta.ID, map[string]any{"t": "session_reopened"})
		return nil
	})
	if err != nil {
		return selected, err
	}
	return store.Info(selected.ID)
}

// ResolveClosed resolves exact IDs or the most recently closed address.
func (s *Store) ResolveClosed(ref string) (Info, error) {
	all, err := s.list(true, false)
	if err != nil {
		return Info{}, err
	}
	ref = strings.TrimSpace(ref)
	var selected Info
	if runstore.IsSessionDirID(ref) {
		selected, err = s.info(ref, false)
		if err != nil {
			return Info{}, err
		}
	} else {
		addr, parseErr := room.ParseAddress(ref)
		if parseErr != nil || addr.Agent == "" {
			return Info{}, errors.New("reopen requires a full room address or session ID")
		}
		for _, info := range all {
			if info.Room == addr.Room && info.EffectiveName() == addr.Agent && info.Status == StatusClosed && (selected.ID == "" || info.Closed.After(selected.Closed)) {
				selected = info
			}
		}
		if selected.ID == "" {
			return Info{}, fmt.Errorf("no closed session has address %q; use a session ID for exact history", ref)
		}
	}

	return selected, nil
}

// pruneClosedPosts keeps exclusions only for posts after the committed cursor.
// A failed read keeps all exclusions so delivery remains conservative.
func (m *Manager) pruneClosedPosts(meta *Meta) {
	if len(meta.ClosedPosts) == 0 {
		return
	}
	var remaining map[string]bool
	cursor := meta.RoomCursor
	for {
		messages, next, err := m.rooms().ReadFrom(meta.RoomID, cursor)
		if err != nil {
			return
		}
		for _, msg := range messages {
			if meta.ClosedPosts[msg.ID] {
				if remaining == nil {
					remaining = map[string]bool{}
				}
				remaining[msg.ID] = true
			}
		}
		if next <= cursor {
			break
		}
		cursor = next
	}
	meta.ClosedPosts = remaining
}
