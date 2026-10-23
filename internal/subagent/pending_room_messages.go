package subagent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/room"
)

type pendingRoomMessage struct {
	room.Message
	Turn int
}

// pendingRoomMessages reads snapshots without acquiring the gate again. Dormant
// close already holds that gate. A claim alone is not proof of delivery.
// Quiet informational posts stay readable but do not require another turn.
func (m *Manager) pendingRoomMessages(meta Meta) ([]pendingRoomMessage, error) {
	claims, err := sessionClaims(m.rooms(), meta)
	if err != nil {
		return nil, err
	}
	delivered, err := m.store().deliveredIDs(meta.ID, meta.Turns)
	if err != nil {
		return nil, err
	}
	for id, done := range meta.InheritedReceipts {
		delivered[id] = delivered[id] || done
	}
	msgs, _, err := m.rooms().ReadAll(meta.RoomID, meta.RoomCursor)
	if err != nil {
		return nil, err
	}
	for id, n := range claims.claimedTurn {
		if n <= 0 || claims.cancelled[id] || claims.dismissed[id] || delivered[id] {
			continue
		}
		msg, err := m.rooms().Message(meta.RoomID, id)
		if errors.Is(err, room.ErrPostNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect claimed message %s (turn %d): %w", id, n, err)
		}
		msgs = append(msgs, msg)
	}
	ordinary := []room.Message{}
	input, err := forSession(m.rooms(), meta, msgs)
	if err != nil {
		return nil, err
	}
	for _, msg := range input {
		if !claims.cancelled[msg.ID] && !claims.dismissed[msg.ID] && !delivered[msg.ID] {
			ordinary = append(ordinary, msg)
		}
	}
	selected, err := m.store().inboxMessages(m.rooms(), meta, ordinary)
	if err != nil {
		return nil, err
	}
	out := []pendingRoomMessage{}
	seen := map[string]bool{}
	// The stored delivery of each post says whether it asks for a turn; the
	// family check keeps the informational notices out, as before.
	rows, err := inputRows(m.rooms(), meta.RoomID, meta.ID, meta.Address(), postIDs(selected))
	if err != nil {
		return nil, err
	}
	for _, msg := range selected {
		if !msg.Deliverable() || !wakes(rows, msg) || claims.dismissed[msg.ID] || seen[msg.ID] || delivered[msg.ID] {
			continue
		}
		seen[msg.ID] = true
		out = append(out, pendingRoomMessage{Message: msg, Turn: claims.claimedTurn[msg.ID]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TS == out[j].TS {
			return out[i].ID < out[j].ID
		}
		return out[i].TS < out[j].TS
	})
	return out, nil
}

func pendingMessagesError(meta Meta, messages []pendingRoomMessage) error {
	details := make([]string, 0, len(messages))
	for _, msg := range messages {
		state := "waiting"
		if msg.Turn > 0 {
			state = fmt.Sprintf("claimed, turn %d, unacknowledged", msg.Turn)
		}
		details = append(details, fmt.Sprintf("%s (from %s; %s; %s; read: dyna room read %s %s; owner dismiss: dyna subagent dismiss-pending %s %s --confirm)", msg.ID, msg.From, time.UnixMilli(msg.TS).UTC().Format(time.RFC3339Nano), state, meta.Room, msg.ID, meta.ID, msg.ID))
	}
	return fmt.Errorf("session %s (%s) has pending messages: %s", meta.ID, meta.Address(), strings.Join(details, "; "))
}
