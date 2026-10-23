package room

// RootDecision says how one post reaches the root seat.
type RootDecision int

const (
	// RootSkip is no root context: the post neither asks the root seat to
	// act nor adds information for it.
	RootSkip RootDecision = iota
	// RootQuiet is held context. The external watch holds it for the next
	// waking post; the internal root reads it with its next real input.
	RootQuiet
	// RootWake asks the root seat to act now. The internal root takes it
	// as turn input; the external watch delivers it with the held context.
	RootWake
)

// Root categories name the quiet sections both roots render from RootContext.
const (
	// RootOwnerDM is the owner's post to an agent, not to the root seat.
	RootOwnerDM = "owner-dm"
	// RootMergeNote is a merge steward's recorded merge decision.
	RootMergeNote = "merge-note"
	// RootQuietPost is any other quiet post addressed to the root seat,
	// whatever its kind: issues, completions and notices all reach root.
	RootQuietPost = "quiet-post"
)

// RootContext classifies one post for the root seat: wake, quiet or skip,
// plus the quiet post's category. Both roots render from this one
// classifier: the external watch holds quiet posts and delivers wake posts,
// and the internal root lists the quiet categories with its next turn that
// has posts. The root's own posts and the Voice call's transcript lines are
// never root context, and the owner's posts to agents stay quiet: the
// agents answer them, and root only learns what was said. Input eligibility
// (RootInput) and wake intent (Message.Wake) are separate. Passive turn input
// has no quiet category because the internal root already reads it in full.
func RootContext(msg Message, roomName string) (RootDecision, string) {
	if msg.Kind == "voice" || msg.From == roomName+":"+RootAgent || msg.From == roomName && !msg.Human {
		return RootSkip, ""
	}
	if !msg.Addressed(roomName) && !msg.Addressed(roomName+":"+RootAgent) {
		if msg.From == roomName && len(msg.To) > 0 {
			return RootQuiet, RootOwnerDM
		}
		return RootSkip, ""
	}
	if RootInput(msg, roomName) {
		if msg.Wake {
			return RootWake, ""
		}
		return RootQuiet, ""
	}
	switch {
	case msg.From == roomName+":merge-steward" && msg.Kind == "merge-decision":
		return RootQuiet, RootMergeNote
	}
	return RootQuiet, RootQuietPost
}
