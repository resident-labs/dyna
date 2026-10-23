package room

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
	// DeliveryDelivered reached the recipient.
	DeliveryDelivered DeliveryState = "delivered"
	// DeliveryCancelled was dropped before it reached the recipient.
	DeliveryCancelled DeliveryState = "cancelled"
	// DeliveryFailed could not reach the recipient.
	DeliveryFailed DeliveryState = "failed"
)

// Delivery is one recipient of one post, as stored in post_deliveries.
// Category is the quiet section of a quiet root-seat delivery, such as
// RootOwnerDM, and empty for every other row.
type Delivery struct {
	SessionID string
	Mode      DeliveryMode
	Category  string
}

// SeatSessionID is the delivery identity of the room's root seat. The seat is
// not a session, and its holder can change, so its rows name the seat rather
// than the session that holds it today.
func SeatSessionID(roomID string) string { return "seat:" + roomID }

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
// A recipient is an address bound to a session in SessionIDs; an address
// without a binding (the owner's own address, mostly) gets no row. The room's
// root seat gets one row when RootContext gives it a wake or a quiet post,
// under SeatSessionID.
//
// The rows are what the post meant for each recipient when it was stored.
// They do not say how far each delivery got: that is DeliveryState.
func DeliveriesFor(msg Message, roomName string) []Delivery {
	var out []Delivery
	mode := deliveryModeFor(msg)
	seen := make(map[string]bool, len(msg.To)+1)
	for _, to := range msg.To {
		id := msg.SessionIDs[to]
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

// seatDeliveryMode maps the root seat's decision to the mode of its row.
// RootSkip never reaches it: a skipped post has no seat row.
func seatDeliveryMode(decision RootDecision) DeliveryMode {
	if decision == RootWake {
		return DeliveryWake
	}
	return DeliveryQuiet
}
