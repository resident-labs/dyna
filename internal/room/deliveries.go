package room

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DeliveryMode says whether a post asks its recipient to act now or only adds
// context for its next turn.
type DeliveryMode string

const (
	// DeliveryWake starts or steers the recipient's session.
	DeliveryWake DeliveryMode = "wake"
	// DeliveryQuiet holds the post for the recipient's next turn.
	DeliveryQuiet DeliveryMode = "quiet"
)

// DeliveryState is how far one delivery got.
type DeliveryState string

const (
	// DeliveryQueued is stored for a recipient that has not claimed it yet.
	DeliveryQueued DeliveryState = "queued"
	// DeliveryClaimed is taken by the recipient's turn and not finished.
	DeliveryClaimed DeliveryState = "claimed"
	// DeliveryDelivered reached the recipient, and a turn receipt says so.
	DeliveryDelivered DeliveryState = "delivered"
	// DeliveryImported comes from the one-time index import of posts that were
	// stored before the index existed. It says the row is not known to have
	// reached its recipient and not known to have missed it: the turn receipts
	// decide. An imported row still takes a claim, a cancellation and a
	// dismissal, so a pending post an owner can see stays theirs to drop.
	DeliveryImported DeliveryState = "imported"
	// DeliveryCancelled was dropped before it reached the recipient.
	DeliveryCancelled DeliveryState = "cancelled"
	// DeliveryFailed could not reach the recipient.
	DeliveryFailed DeliveryState = "failed"
	// DeliveryDismissed was dropped by the owner before it reached the
	// recipient. The post stays in the room log; the recipient never reads it.
	DeliveryDismissed DeliveryState = "dismissed"
)

// DeliveryHow says how a stored delivery reached its recipient.
type DeliveryHow string

const (
	// DeliveryWoke means this delivery started the recipient's turn.
	DeliveryWoke DeliveryHow = "woke"
	// DeliveryBatched means the post was included in a turn something else started.
	DeliveryBatched DeliveryHow = "batched"
	// DeliveryQuietContext means the post waited as context and did not start a turn.
	DeliveryQuietContext DeliveryHow = "quiet"
)

// Delivery is one recipient of one post, as stored in post_deliveries.
// PostID names the post the delivery carries, and is empty in the deliveries
// that DeliveriesFor builds for a post it is about to index. Category is the
// quiet section of a quiet root-seat delivery, such as RootOwnerDM, and empty
// for every other row.
type Delivery struct {
	PostID      string
	SessionID   string
	Mode        DeliveryMode
	Category    string
	State       DeliveryState
	ClaimedTurn int
	// How is set when a turn takes the delivery. Empty means it has not been taken.
	How DeliveryHow
	// DeliveredAt is the unix-millisecond time the recipient's turn took the
	// post. Zero means it has not been taken. It is a clock time, not the
	// row version in updated_at.
	DeliveredAt int64
}

// HowText is the owner-facing delivery phrase: woke, batched into turn N, or
// quiet context. Empty means the delivery has not been taken.
func (d Delivery) HowText() string {
	switch d.How {
	case DeliveryWoke:
		return "woke"
	case DeliveryBatched:
		if d.ClaimedTurn > 0 {
			return fmt.Sprintf("batched into turn %d", d.ClaimedTurn)
		}
		return "batched"
	case DeliveryQuietContext:
		return "quiet context"
	default:
		return ""
	}
}

// Summary is one recipient line for dyna room read: who, how, and when.
func (d Delivery) Summary(label string) string {
	how := d.HowText()
	if how == "" {
		if d.State == DeliveryQueued || d.State == "" || d.State == DeliveryImported {
			return label + ": not delivered yet"
		}
		how = string(d.Mode)
	}
	if d.DeliveredAt > 0 {
		return fmt.Sprintf("%s: %s at %s", label, how, time.UnixMilli(d.DeliveredAt).Local().Format("2006-01-02 15:04:05"))
	}
	return label + ": " + how
}

// DeliveryLabel is the recipient name a person reads. A root-seat row names
// the room's root, not the seat key.
func DeliveryLabel(sessionID, roomName string) string {
	if strings.HasPrefix(sessionID, seatPrefix) {
		if roomName == "" {
			return "root"
		}
		return roomName + ":root"
	}
	return sessionID
}

// DeliveryReceipt is the dyna post line for one session delivery. A quiet
// post queues no turn, so it must not say session-queued.
func DeliveryReceipt(mode DeliveryMode) string {
	if mode == DeliveryQuiet {
		return "session-quiet"
	}
	return "session-queued"
}

// WakeSender is the short name in "woke on <post> from <sender>".
func WakeSender(msg Message) string {
	if msg.Human {
		return "the owner"
	}
	if msg.Script != nil {
		if msg.Script.Workflow != "" {
			return msg.Script.Workflow
		}
		if msg.Script.RunID != "" {
			return msg.Script.RunID
		}
	}
	if msg.Kind == string(FamilyCheckin) {
		return "parent check-in"
	}
	if msg.Kind == string(FamilyEngine) && msg.Notice.Kind == string(TypeRootFlush) {
		return "a schedule"
	}
	if msg.From == "" {
		return "the engine"
	}
	return msg.From
}

// WakeLine is the short line a turn and a room log show for a post that
// started a turn.
func WakeLine(postID, sender string) string {
	if postID == "" {
		return ""
	}
	if sender == "" {
		sender = "the engine"
	}
	return fmt.Sprintf("woke on %s from %s", postID, sender)
}

// RootInput reports whether a root-seat delivery is turn input: a post that
// asks the seat to act, or a passive one that only adds context to its next
// turn. A quiet post with a category has its own section in root's context
// instead of being turn input.
func (d Delivery) RootInput() bool { return d.Mode == DeliveryWake || d.Category == "" }

// SeatSessionID is the delivery identity of the room's root seat. The seat is
// not a session, and its holder can change, so its rows name the seat rather
// than the session that holds it today.
func SeatSessionID(roomID string) string { return seatPrefix + roomID }

// seatPrefix starts the seat's delivery identity and its DM conversation.
const seatPrefix = "seat:"

// seatSessionRe is the shape of a root seat's delivery identity: the seat of
// one well-formed room.
var seatSessionRe = regexp.MustCompile(`^seat:room_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$`)

// deliveryModeFor is the mode of every delivery of msg: a post that does not
// ask for a wake, and an event of a quiet family, only add context.
func deliveryModeFor(msg Message) DeliveryMode {
	if !msg.Wake {
		return DeliveryQuiet
	}
	if spec, ok := eventSpecFor(msg.Kind, msg.Notice.Kind); ok && spec.Quiet {
		return DeliveryQuiet
	}
	return DeliveryWake
}

// DeliveriesFor lists the recipients of one stored post, each with its mode.
// A recipient is an address bound to a session in SessionIDs, keyed by that
// session ID, or an agent address without a binding, keyed by the address text
// (see deliveryRowKey). A room address names the owner, who is no session, and
// gets no row. The room's root seat gets one row when RootContext gives it a
// wake or a quiet post, under SeatSessionID.
//
// The rows are what the post meant for each recipient when it was stored.
// They do not say how far each delivery got: that is DeliveryState.
func DeliveriesFor(msg Message, roomName string) []Delivery {
	var out []Delivery
	mode := deliveryModeFor(msg)
	seen := make(map[string]bool, len(msg.To)+1)
	for _, to := range msg.To {
		id := deliveryRowKey(msg, roomName, to)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Delivery{SessionID: id, Mode: mode})
	}
	if decision, category := RootContext(msg, roomName); decision != RootSkip {
		out = append(out, Delivery{SessionID: SeatSessionID(msg.RoomID), Mode: seatDeliveryMode(decision), Category: category})
	}
	return out
}

// deliveryRowKey is the session_id the row for recipient to carries: the
// session bound to the address, or the address itself when the post bound no
// session to it. A post that names an address without a binding still reaches
// the session that answers to that address, so the row keeps the text that
// matched. It names no session, so a rename ends the match, as the address text
// did before the rows existed. The room's own address names the owner, who is
// no session: the room's root seat reads its own row instead.
func deliveryRowKey(msg Message, roomName, to string) string {
	if id := msg.SessionIDs[to]; id != "" {
		return id
	}
	if to == roomName {
		return ""
	}
	return to
}

// seatDeliveryMode maps the root seat's decision to the mode of its row.
// RootSkip never reaches it: a skipped post has no seat row.
func seatDeliveryMode(decision RootDecision) DeliveryMode {
	if decision == RootWake {
		return DeliveryWake
	}
	return DeliveryQuiet
}
