package contract

import "time"

// EventType is "family.verb" in the past tense, such as "post.created".
type EventType string

type Event struct {
	Seq  int64     `json:"seq"` // total order in the store, set at commit
	ID   EventID   `json:"id"`
	Time time.Time `json:"at"`
	Room RoomID    `json:"room,omitempty"` // empty for machine events
	From Principal `json:"from"`
	Body EventBody `json:"body"` // the JSON form also carries "type"
}

// EventBody is one registered body struct. The unexported method seals the
// set, so every body is defined in contract/.
type EventBody interface {
	EventType() EventType
	isEventBody()
}

type Surface string

const (
	SurfaceFeed    Surface = "feed"    // a post in the room feed, by anyone
	SurfaceDM      Surface = "dm"      // the owner, in this session's DM
	SurfaceCard    Surface = "card"    // the owner, through an HTML card
	SurfaceSession Surface = "session" // the engine, from another session's turn: a brief or a result
	SurfaceService Surface = "service" // a reviewer or helper service (M3)
	SurfaceEngine  Surface = "engine"  // a system event
)

type ConversationKind string // "feed" | "dm"

type Conversation struct {
	Kind    ConversationKind `json:"kind"`
	Session SessionID        `json:"session,omitempty"` // dm only
}

type ReplyKind string

const (
	ReplyFinal ReplyKind = "final" // the engine delivers the final response to Target
	ReplyPost  ReplyKind = "post"  // answer with dyna post, only if needed
	ReplyNone  ReplyKind = "none"  // nothing is owed; end with one line
)

type ReplyChannel struct {
	Kind    ReplyKind    `json:"kind"`
	Target  Principal    `json:"target,omitempty"`
	Home    Conversation `json:"home,omitempty"`
	ReplyTo EventID      `json:"replyTo,omitempty"`
}

type Delivery struct {
	Seq     int64        `json:"seq"` // the event
	To      Principal    `json:"to"`  // a session or a member
	Surface Surface      `json:"surface"`
	Reply   ReplyChannel `json:"reply"`
	Wake    bool         `json:"wake"` // starts or steers a turn; members never wake
}

type BoxState string

const (
	BoxPending  BoxState = "pending"
	BoxCreating BoxState = "creating"
	BoxStarting BoxState = "starting"
	BoxReady    BoxState = "ready"
	BoxStopped  BoxState = "stopped"
	BoxClosing  BoxState = "closing"
	BoxDeleted  BoxState = "deleted"
	BoxFailed   BoxState = "failed"
	BoxLost     BoxState = "lost"
)

type BoxSizeName string

const (
	BoxSizeSmall  BoxSizeName = "small"
	BoxSizeMedium BoxSizeName = "medium"
	BoxSizeLarge  BoxSizeName = "large"
)

type BoxSize struct {
	Name     BoxSizeName `json:"name"`
	CPUs     int         `json:"cpus"`
	MemBytes int64       `json:"memBytes"`
	Pids     int         `json:"pids"`
}

func (PostCreated) EventType() EventType { return "post.created" }
func (PostCreated) isEventBody()         {}

func (SessionStarted) EventType() EventType { return "session.started" }
func (SessionStarted) isEventBody()         {}

func (SessionClosed) EventType() EventType { return "session.closed" }
func (SessionClosed) isEventBody()         {}

func (TurnStarted) EventType() EventType { return "turn.started" }
func (TurnStarted) isEventBody()         {}

func (TurnEnded) EventType() EventType { return "turn.ended" }
func (TurnEnded) isEventBody()         {}

func (BoxChanged) EventType() EventType { return "box.changed" }
func (BoxChanged) isEventBody()         {}
