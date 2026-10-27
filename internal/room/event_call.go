package room

// CallState is how the owner answered a call request: they joined it, they
// declined it, or the request was withdrawn before they answered.
type CallState string

// Call states.
const (
	CallJoined    CallState = "joined"
	CallDeclined  CallState = "declined"
	CallWithdrawn CallState = "withdrawn"
)

// callStates lists every call state the web needs as a union type.
var callStates = []CallState{CallJoined, CallDeclined, CallWithdrawn}

// CallEvent tells the room how one call request ended. It carries the
// request's ID, so the card names the call without reading the post text.
type CallEvent struct {
	Type CallState `json:"type"`
	// Call is the request ID the owner's call store made.
	Call string `json:"call"`
}

func (CallEvent) eventFamily() EventFamily { return FamilyCall }
func (c CallEvent) eventType() EventType   { return EventType(c.Type) }

// eventSubject is the request, so the answer of one call is one row.
func (c CallEvent) eventSubject() string { return c.Call }

// eventDedupe keys one post per request and answer: a retried close delivery
// posts once, and a request that rings again is its own post.
func (c CallEvent) eventDedupe() string { return string(c.Type) + "\x00" + c.Call }
