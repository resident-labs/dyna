package subagent

import (
	"context"
	"dyna-agent/internal/database"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
)

// InputClaim asks the engine to accept input for the caller's current turn.
// Startup drains the initial prompt; other reads serve live steering.
type InputClaim struct {
	Turn          int      `json:"turn"`
	Startup       bool     `json:"startup,omitempty"`
	Final         bool     `json:"final,omitempty"`
	Cursor        int64    `json:"cursor,omitempty"`
	Covered       []string `json:"covered,omitempty"`
	Depth         int      `json:"depth,omitempty"`
	ParentSession string   `json:"parentSession,omitempty"`
}

// ClaimedPost is input the engine committed before returning it. The frame
// is built from the engine's stored sender, recipient and hierarchy.
type ClaimedPost struct {
	Steer Steer  `json:"steer"`
	From  string `json:"from,omitempty"`
	Owed  string `json:"owed,omitempty"`
}

// ClaimedInput returns the selected input or control result. A claim that
// accepts work keeps this exact JSON value in its request receipt.
type ClaimedInput struct {
	Posts         []ClaimedPost `json:"posts,omitempty"`
	Cursor        int64         `json:"cursor"`
	Depth         int           `json:"depth,omitempty"`
	ParentSession string        `json:"parentSession,omitempty"`
	Closed        bool          `json:"closed,omitempty"`
	readOnly      bool
	roomID        string
}

// RequestReadOnly is the selector's private receipt decision. JSON cannot
// set it: input and control mutations always keep their exact receipt.
func (r ClaimedInput) RequestReadOnly() bool { return r.readOnly }

func (m *Manager) RunnerClaimInput(ctx context.Context, input InputClaim) (ClaimedInput, error) {
	id, err := runnerSession(ctx)
	if err != nil {
		return ClaimedInput{}, err
	}
	return m.claimInput(ctx, id, input)
}

// claimInput is the one startup/live selection and claim boundary. It holds
// only the engine's gate lock; callers never hold their gate across the call.
func (m *Manager) claimInput(ctx context.Context, id string, input InputClaim) (ClaimedInput, error) {
	return m.selectInput(ctx, id, input, false)
}

// selectInput shares eligibility and framing between a read-only wait and
// the authoritative claim. Only a claim commits the selected effects.
func (m *Manager) selectInput(ctx context.Context, id string, input InputClaim, readOnly bool) (ClaimedInput, error) {
	if input.Turn <= 0 || input.Startup && input.Final || !input.Startup && len(input.Covered) > 0 {
		return ClaimedInput{}, errors.New("invalid runner input claim")
	}
	store := m.store()
	var reply ClaimedInput
	err := store.withGateLockContext(ctx, id, func(dir string) error {
		var reader inboxReader = store
		if readOnly {
			db, err := database.Open(filepath.Dir(store.root()))
			if err != nil {
				return err
			}
			reader = canonicalInputReader{Store: store, ctx: ctx, db: db}

		}
		meta, err := reader.ReadMeta(id)
		if err != nil {
			return err
		}
		if !meta.Closed.IsZero() {
			return ErrClosed
		}
		if meta.Turns != input.Turn {
			return errors.New("input claim does not name the session's current turn")
		}
		if readOnly {
			if _, _, err := reader.readTurnRow(id, input.Turn); err != nil {
				return err
			}
		}

		reply = ClaimedInput{Cursor: input.Cursor, Depth: meta.Depth, ParentSession: meta.ParentSession, roomID: meta.RoomID}
		var q queueClaims
		if readOnly {
			q, err = readCanonicalQueue(dir)
		} else {
			q, err = readQueueForUpdate(dir)
		}
		if err != nil {
			return err
		}
		changed := false
		commit := func() error {
			if readOnly {
				return nil
			}
			// An empty final read closes only after all earlier claims have
			// resolved. Sealing changes no queue row. If recording its reply
			// fails, a retry reads the same closed gate and cursor.
			if input.Final && len(reply.Posts) == 0 && !reply.Closed {
				st := readGateState(dir)
				st.Active, st.ClosedAt = false, reply.Cursor
				if err := writeGateState(dir, st); err != nil {
					return err
				}
				reply.Closed = true
			}
			if !changed && len(reply.Posts) == 0 && !reply.Closed {
				// An empty read changed no queue or gate. A retry may check
				// again; there is no accepted input or control effect to replay.
				reply.readOnly = true
				return nil
			}
			var raw []byte
			if changed {
				raw, err = json.Marshal(q)
				if err != nil {
					return err
				}
			}
			result, err := json.Marshal(reply)
			if err != nil {
				return err
			}
			return storeQueueContext(ctx, dir, raw, result)
		}

		var selected []room.Message
		paused, err := m.rooms().Paused(meta.RoomID)
		if err != nil {
			return err
		}
		if input.Startup {
			reply.Cursor = meta.RoomCursor
			turn, err := store.ReadTurn(id, input.Turn)
			if err != nil {
				return err
			}
			// Replay the saved attempt exactly. A refreshed snapshot must
			// not cancel notes or claim input beyond that held prompt.
			if turn.HeldPrompt != "" || paused {
				return commit()
			}
			for _, note := range input.Covered {
				if !q.Cancelled[note] {
					q.Cancelled[note], changed = true, true
				}
			}
			selected, reply.Cursor, err = m.drainRoomMessages(meta, turn)
		} else {
			st := readGateState(dir)
			if st.Turn != input.Turn {
				return errors.New("input gate belongs to another turn")
			}
			if !st.Active || st.Pid <= 0 {
				reply.Closed = true
				if st.Turn == input.Turn && !st.Active {
					reply.Cursor = st.ClosedAt
				}
				return commit()
			}
			if meta.Depth != input.Depth || meta.ParentSession != input.ParentSession {
				if readOnly {
					reply.Posts = append(reply.Posts, ClaimedPost{})
				} else {
					reply.Posts = append(reply.Posts, ClaimedPost{Steer: Steer{TS: time.Now().UnixMilli(), Message: "[DYNA HIERARCHY CHANGED] This replaces earlier parent and depth instructions. Continue your active task. Use the current hierarchy below. The CLI checks the current depth limit when you start a child.\n" + store.SelectorContext(meta)}})
				}
			}
			if paused {
				return commit()
			}
			var messages []room.Message
			messages, reply.Cursor, err = m.rooms().ReadFrom(meta.RoomID, input.Cursor)
			if err == nil {
				cache := store.inputInbox(id, input.Turn)
				cache.Lock()
				defer cache.Unlock()
				cache.cache.turn, cache.cache.roomEnd = input.Turn, reply.Cursor
				selected, err = store.cachedInboxMessages(m.rooms(), meta, forSession(m.rooms(), meta, messages), &cache.cache, reader)
			}
		}
		if err != nil {
			return err
		}
		for _, msg := range selected {
			if q.Dismissed[msg.ID] || !input.Startup && q.Claimed[msg.ID] == input.Turn {
				continue
			}
			if q.Cancelled[msg.ID] && len(meta.InheritedInboxes) == 0 {
				continue
			}
			if readOnly {
				reply.Posts = append(reply.Posts, ClaimedPost{})
				return commit()
			}
			q.claim(msg.ID, input.Turn)
			changed = true
			frame := msg.Frame(store.frameOptions(meta, msg))
			if !input.Startup {
				frame, _ = msg.SteeringFrame(store.frameOptions(meta, msg), runstore.MaxSteeringMessageBytes)
			}
			post := ClaimedPost{Steer: Steer{ID: msg.ID, TS: msg.TS, Message: frame}, From: msg.From}
			if store.Owes(meta, msg) {
				post.Owed = msg.From
			}
			reply.Posts = append(reply.Posts, post)
		}
		return commit()
	})
	if err != nil {
		return ClaimedInput{}, err
	}
	return reply, nil
}

func (s *Store) inputInbox(id string, turn int) *readInbox {
	return readInboxFor(fmt.Sprintf("%s\x00%s/turn/%d", s.root(), id, turn), false)
}
