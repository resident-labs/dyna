package contract

type Surface string

const (
	SurfaceFeed    Surface = "feed"    // a post in the room feed, by anyone
	SurfaceDM      Surface = "dm"      // the owner, in this session's DM
	SurfaceCard    Surface = "card"    // the owner, through an HTML card
	SurfaceSession Surface = "session" // the engine, from another session's turn: a brief or a result
	SurfaceService Surface = "service" // a helper service (M3)
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
	ReplyNone  ReplyKind = "none"  // nothing is owed, so it must not stop work in progress
)

type ReplyChannel struct {
	Kind    ReplyKind    `json:"kind"`
	Target  Principal    `json:"target,omitempty"`
	Home    Conversation `json:"home,omitempty"`
	ReplyTo EventID      `json:"replyTo,omitempty"`
}

type Delivery struct {
	Seq         int64        `json:"seq"` // the event
	To          Principal    `json:"to"`  // a session
	Surface     Surface      `json:"surface"`
	Reply       ReplyChannel `json:"reply"`
	Wake        bool         `json:"wake"` // starts or steers a turn
	HandoffFrom SessionID    `json:"handoffFrom,omitempty"`
}

const (
	ConversationFeed ConversationKind = "feed"
	ConversationDM   ConversationKind = "dm"
)
