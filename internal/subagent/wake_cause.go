package subagent

import (
	"context"
	"log"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
)

// WakeKind is why a turn started. It is a closed list so a reader never has
// to guess from free text (rule r7, rule r8).
type WakeKind string

const (
	// WakePost means a room post started the turn.
	WakePost WakeKind = "post"
	// WakeTimer means a schedule or timed flush started the turn.
	WakeTimer WakeKind = "timer"
	// WakeCheckin means a parent check-in started the turn.
	WakeCheckin WakeKind = "checkin"
	// WakeResume means a resume, such as a model transfer, started the turn.
	WakeResume WakeKind = "resume"
	// WakeWorkflow means a workflow run started the turn.
	WakeWorkflow WakeKind = "workflow"
	// WakeOwner means a person or a parent started the session.
	WakeOwner WakeKind = "owner"
	// WakeAssignment means a service gave the session its next assignment.
	WakeAssignment WakeKind = "assignment"
	// WakeWrapUp means the engine's wrap-up check started the turn.
	WakeWrapUp WakeKind = "wrap-up"
)

// WakeCause is the typed fact stored with a turn: what started it.
type WakeCause struct {
	Kind   WakeKind `json:"kind,omitempty"`
	PostID string   `json:"postId,omitempty"`
	Sender string   `json:"sender,omitempty"`
	Name   string   `json:"name,omitempty"`
}

// Line is the short line the session, the room log and the session view show.
func (c WakeCause) Line() string {
	who := c.who()
	switch c.Kind {
	case "":
		return ""
	case WakePost:
		return room.WakeLine(c.PostID, who)
	case WakeTimer:
		if c.PostID != "" {
			return room.WakeLine(c.PostID, who)
		}
		return "woke on a schedule from " + who
	case WakeCheckin:
		if c.PostID != "" {
			return room.WakeLine(c.PostID, who)
		}
		return "woke on a parent check-in from " + who
	case WakeResume:
		return "woke on a resume from " + who
	case WakeWorkflow:
		if c.PostID != "" {
			return room.WakeLine(c.PostID, who)
		}
		return "woke on a workflow from " + who
	case WakeOwner:
		return "woke on a start from " + who
	case WakeAssignment:
		return "woke on an assignment from " + who
	case WakeWrapUp:
		return "woke on a wrap-up check from " + who
	default:
		if c.PostID != "" {
			return room.WakeLine(c.PostID, who)
		}
		return "woke on " + string(c.Kind) + " from " + who
	}
}

func (c WakeCause) who() string {
	if c.Sender != "" {
		return c.Sender
	}
	if c.Name != "" {
		return c.Name
	}
	return "the engine"
}

// HeaderLine is the sentence a session reads in its turn header.
func (c WakeCause) HeaderLine() string {
	if c.Line() == "" {
		return ""
	}
	return "This turn " + c.Line() + "."
}

func causeFromMessage(msg room.Message) WakeCause {
	kind := WakePost
	switch {
	case msg.Script != nil:
		kind = WakeWorkflow
	case msg.Kind == string(room.FamilyCheckin):
		kind = WakeCheckin
	case msg.Kind == string(room.FamilyEngine) && msg.Notice.Kind == string(room.TypeRootFlush):
		kind = WakeTimer
	}
	return WakeCause{Kind: kind, PostID: msg.ID, Sender: room.WakeSender(msg)}
}

func (m *Manager) wakeCause(meta Meta, messageID, transferID string, wrapUp, first bool) WakeCause {
	if messageID != "" {
		msg, err := m.rooms().Message(meta.RoomID, messageID)
		if err != nil {
			return WakeCause{Kind: WakePost, PostID: messageID, Sender: "unknown"}
		}
		return causeFromMessage(msg)
	}
	if transferID != "" {
		return WakeCause{Kind: WakeResume, Sender: "model transfer"}
	}
	if wrapUp {
		return WakeCause{Kind: WakeWrapUp, Sender: "the engine"}
	}
	if first && meta.RunOwned() {
		name := meta.Run.ID
		return WakeCause{Kind: WakeWorkflow, Name: name, Sender: name}
	}
	if first {
		sender := "the owner"
		if meta.ParentSession != "" {
			sender = "its parent"
			if parent, err := m.store().ReadMeta(meta.ParentSession); err == nil {
				sender = parent.Address()
			}
		} else if meta.Owner != "" {
			sender = meta.Owner
		}
		return WakeCause{Kind: WakeOwner, Sender: sender}
	}
	return WakeCause{Kind: WakeAssignment, Sender: "the engine"}
}

// noteSet is the delivery rows a note may update. Root also notes the seat
// row, which is the row room read shows for a post to the root seat.
func noteSet(meta Meta) room.DeliverySet {
	set := deliverySet(meta)
	if meta.Role == RoleRoot {
		set.Keys = append(set.Keys, room.SeatSessionID(meta.RoomID))
	}
	return set
}

func (m *Manager) noteHow(ctx context.Context, meta Meta, postIDs []string, how room.DeliveryHow, turn int) error {
	if len(postIDs) == 0 {
		return nil
	}
	// The claim already published the session change. A second event would
	// wake readers twice for one delivery.
	return m.rooms().NoteDeliveries(ctx, hub.Event{}, noteSet(meta), postIDs, how, turn, time.Now().UnixMilli())
}

// deliveryHowGroups says how this turn took each post. The post that started
// the turn stays woke. An empty list needs no read.
func (m *Manager) deliveryHowGroups(meta Meta, postIDs []string, turn int) (map[room.DeliveryHow][]string, error) {
	if len(postIDs) == 0 {
		return nil, nil
	}
	rows, err := inputRows(m.rooms(), meta.RoomID, meta.ID, meta.Address(), postIDs)
	if err != nil {
		return nil, err
	}
	var seats map[string]room.Delivery
	if meta.Role == RoleRoot {
		seats, err = m.rooms().SeatDeliveries(meta.RoomID, postIDs)
		if err != nil {
			return nil, err
		}
	}
	stored, err := m.store().ReadTurn(meta.ID, turn)
	if err != nil {
		return nil, err
	}
	groups := map[room.DeliveryHow][]string{}
	for _, postID := range postIDs {
		mode := room.DeliveryWake
		if row, ok := rows[postID]; ok {
			mode = row.Mode
		}
		if seat, ok := seats[postID]; ok {
			mode = seat.Mode
		}
		how := room.DeliveryBatched
		switch {
		case mode == room.DeliveryQuiet:
			how = room.DeliveryQuietContext
		case stored.Wake.PostID == postID:
			how = room.DeliveryWoke
		}
		groups[how] = append(groups[how], postID)
	}
	return groups, nil
}

// noteIncluded records how this turn took each post it included. The claim
// path writes the same groups in its own transaction. This remains for a
// caller that notes posts the claim did not take.
func (m *Manager) noteIncluded(ctx context.Context, id string, turn int, reply ClaimedInput) error {
	var ids []string
	for _, post := range reply.Posts {
		if post.Steer.ID != "" {
			ids = append(ids, post.Steer.ID)
		}
	}
	meta, err := m.store().ReadMeta(id)
	if err != nil {
		return err
	}
	groups, err := m.deliveryHowGroups(meta, ids, turn)
	if err != nil {
		return err
	}
	for how, posts := range groups {
		if err := m.noteHow(ctx, meta, posts, how, turn); err != nil {
			return err
		}
	}
	return nil
}

// inputFacts adds sent time, delivered time and waking or quiet to each
// post frame. Empty options mean the caller should build them itself.
func (m *Manager) inputFacts(meta Meta, msgs []room.Message) map[string]room.FrameOptions {
	ids := postIDs(msgs)
	rows, err := inputRows(m.rooms(), meta.RoomID, meta.ID, meta.Address(), ids)
	if err != nil {
		log.Printf("session %s: input facts: %v", meta.ID, err)
	}
	var seats map[string]room.Delivery
	if meta.Role == RoleRoot {
		seats, err = m.rooms().SeatDeliveries(meta.RoomID, ids)
		if err != nil {
			log.Printf("session %s: seat input facts: %v", meta.ID, err)
		}
	}
	now := time.Now()
	out := make(map[string]room.FrameOptions, len(msgs))
	for _, msg := range msgs {
		options := m.store().frameOptions(meta, msg)
		options.Sent = time.UnixMilli(msg.TS)
		options.Delivered = now
		mode := room.DeliveryWake
		if row, ok := rows[msg.ID]; ok {
			mode = row.Mode
		} else if !msg.Wake {
			mode = room.DeliveryQuiet
		}
		if seat, ok := seats[msg.ID]; ok {
			mode = seat.Mode
		}
		options.Input = "waking"
		if mode == room.DeliveryQuiet {
			options.Input = "quiet"
		}
		out[msg.ID] = options
	}
	return out
}
