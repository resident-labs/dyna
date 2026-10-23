package subagent

import (
	"context"
	"dyna-agent/internal/database"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
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

// claimInput is the one startup/live selection and claim boundary. Callers
// never hold their gate across the call.
func (m *Manager) claimInput(ctx context.Context, id string, input InputClaim) (ClaimedInput, error) {
	return m.selectInput(ctx, id, input, false)
}

// selectInput shares eligibility and framing between a read-only wait and
// the authoritative claim. Only a claim commits the selected effects.
func (m *Manager) selectInput(ctx context.Context, id string, input InputClaim, readOnly bool) (ClaimedInput, error) {
	if input.Turn <= 0 || input.Startup && input.Final || !input.Startup && len(input.Covered) > 0 {
		return ClaimedInput{}, errors.New("invalid runner input claim")
	}
	frames := inputFrames{}
	for {
		reply, err := m.selectPreparedInput(ctx, id, input, readOnly, &frames)
		if !errors.Is(err, errInputFrames) {
			return reply, err
		}
		// Delivery aliases and reply addresses can read other sessions. Resolve
		// them with the gate released, then recheck eligibility before claiming.
		for _, msg := range frames.pending {
			options := m.store().frameOptions(frames.meta, msg)
			frame := msg.Frame(options)
			if !input.Startup {
				frame, _ = msg.SteeringFrame(options, runstore.MaxSteeringMessageBytes)
			}
			post := ClaimedPost{Steer: Steer{ID: msg.ID, TS: msg.TS, Message: frame}, From: msg.From}
			if m.store().Owes(frames.meta, msg) {
				post.Owed = msg.From
			}
			frames.posts[msg.ID] = post
		}
		frames.pending = nil
		if frames.needHierarchy {
			frames.hierarchy = m.store().SelectorContext(frames.meta)
			frames.needHierarchy = false
		}
	}
}

var errInputFrames = errors.New("input frames need delivery resolution")

type inputFrames struct {
	meta          Meta
	posts         map[string]ClaimedPost
	pending       []room.Message
	hierarchy     string
	needHierarchy bool
}

func (m *Manager) selectPreparedInput(ctx context.Context, id string, input InputClaim, readOnly bool, frames *inputFrames) (ClaimedInput, error) {
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

		// A rename, transfer or consolidation can change framing while the gate
		// is released. Discard prepared frames when session metadata changes.
		if !readOnly && !reflect.DeepEqual(frames.meta, meta) {
			*frames = inputFrames{meta: meta, posts: map[string]ClaimedPost{}}
		}
		reply = ClaimedInput{Cursor: input.Cursor, Depth: meta.Depth, ParentSession: meta.ParentSession, roomID: meta.RoomID}
		// covered names the notes a held prompt already carried; claimed names
		// the posts this read hands to the turn. Both are committed with the
		// reply, so a retry replays them and a crash repeats nothing.
		var covered, claimed []string
		commit := func() error {
			if readOnly {
				return nil
			}
			// An empty final read closes only after all earlier claims have
			// resolved. Sealing changes no delivery row. If recording its reply
			// fails, a retry reads the same closed gate and cursor.
			if input.Final && len(reply.Posts) == 0 && !reply.Closed {
				st := readGateState(dir)
				st.Active, st.ClosedAt = false, reply.Cursor
				if err := writeGateState(dir, st); err != nil {
					return err
				}
				reply.Closed = true
			}
			if len(claimed) == 0 && len(covered) == 0 && len(reply.Posts) == 0 && !reply.Closed {
				// An empty read changed no delivery and no gate. A retry may check
				// again; there is no accepted input or control effect to replay.
				reply.readOnly = true
				return nil
			}
			result, err := json.Marshal(reply)
			if err != nil {
				return err
			}
			return storeClaimReply(ctx, m.rooms(), meta, claimed, covered, input.Turn, result)
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
			covered = input.Covered
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
					if frames.hierarchy == "" {
						frames.needHierarchy = true
						return errInputFrames
					}
					reply.Posts = append(reply.Posts, ClaimedPost{Steer: Steer{TS: time.Now().UnixMilli(), Message: "[DYNA HIERARCHY CHANGED] This replaces earlier parent and depth instructions. Continue your active task. Use the current hierarchy below. The CLI checks the current depth limit when you start a child.\n" + frames.hierarchy}})
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
				// The cache moves only once its rows are read, so a failed read
				// leaves the cached pending posts where they are.
				var ordinary []room.Message
				if ordinary, err = forSession(m.rooms(), meta, messages); err == nil {
					cache.cache.turn, cache.cache.roomEnd = input.Turn, reply.Cursor
					selected, err = store.cachedInboxMessages(m.rooms(), meta, ordinary, &cache.cache, reader)
				}
			}
		}
		if err != nil {
			return err
		}
		claims, err := postClaims(m.rooms(), meta, append(postIDs(selected), covered...))
		if err != nil {
			return err
		}
		// A note the held prompt already carried is cancelled in this read's
		// commit, so this read must not hand it to the turn again. A note that is
		// already cancelled needs no write, and the reply then stays read-only.
		var notes []string
		for _, note := range covered {
			if claims.cancelled[note] {
				continue
			}
			claims.cancelled[note] = true
			notes = append(notes, note)
		}
		covered = notes
		var eligible []room.Message
		for _, msg := range selected {
			if claims.dismissed[msg.ID] || !input.Startup && claims.claimedTurn[msg.ID] == input.Turn {
				continue
			}
			if claims.cancelled[msg.ID] && len(meta.InheritedInboxes) == 0 {
				continue
			}
			if readOnly {
				reply.Posts = append(reply.Posts, ClaimedPost{})
				return commit()
			}
			eligible = append(eligible, msg)
			if _, ok := frames.posts[msg.ID]; !ok {
				frames.pending = append(frames.pending, msg)
			}
		}
		if len(frames.pending) > 0 {
			return errInputFrames
		}
		for _, msg := range eligible {
			claimed = append(claimed, msg.ID)
			reply.Posts = append(reply.Posts, frames.posts[msg.ID])
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
