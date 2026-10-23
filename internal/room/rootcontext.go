package room

import "strings"

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

// Root categories name the quiet sections both roots render from the seat's
// quiet deliveries.
const (
	// RootOwnerDM is the owner's post to an agent, not to the root seat.
	RootOwnerDM = "owner-dm"
	// RootQuietPost is any other quiet post addressed to the root seat,
	// whatever its kind: issues, completions and notices all reach root.
	RootQuietPost = "quiet-post"
)

// RootContext classifies one post for the root seat: wake, quiet or skip,
// plus the quiet post's category. It is the one write-time classifier: notify
// stores each post's seat delivery row from it, and every reader reads that
// row instead of classifying the post again. The root's own posts and the
// Voice call's transcript lines are never root context, and the owner's posts
// to agents stay quiet: the agents answer them, and root only learns what was
// said. Wake intent is the post's own Wake field; passive turn input has no
// quiet category, because the internal root reads it in full.
func RootContext(msg Message, roomName string) (RootDecision, string) {
	// The Voice call's lines stay in the call, and the seat's own posts never
	// come back to it. An orchestrator's own post as the owner is an echo too,
	// with two exceptions: a post that needs root, and one addressed to the root
	// session itself, which the owner's shell and API posts reach directly.
	if msg.Kind == "voice" || msg.From == roomName+":"+RootAgent {
		return RootSkip, ""
	}
	if msg.From == roomName && !msg.Human && !msg.EngineEvent() && !msg.Addressed(roomName+":"+RootAgent) && !msg.NeedsRoot {
		return RootSkip, ""
	}
	if !msg.Addressed(roomName) && !msg.Addressed(roomName+":"+RootAgent) {
		// Only the owner speaks as the room. A room named after the engine holds
		// engine posts under that same address, so an engine event is never an
		// owner DM: a quiet session note belongs to the session it names.
		if msg.From == roomName && !msg.EngineEvent() && len(msg.To) > 0 {
			return RootQuiet, RootOwnerDM
		}
		return RootSkip, ""
	}
	if rootInput(msg, roomName) {
		if msg.Wake {
			return RootWake, ""
		}
		return RootQuiet, ""
	}
	return RootQuiet, RootQuietPost
}

// rootInput reports whether the post is the root seat's input: it asks the
// seat to act, or it is a passive engine note that the seat drains with its
// next real input. Passive notes keep their Wake field false.
func rootInput(msg Message, roomName string) bool {
	// A check-in is input for the parent it names, whoever posted it: the
	// engine now, and "system:parent-checkin" for a post stored before the
	// typed events. The family check covers both.
	return rootWakes(msg, roomName) || msg.Addressed(roomName+":"+RootAgent) && (msg.From == roomName || msg.From == "dyna" || msg.Kind == string(FamilyCheckin))
}

// rootWakes reports whether a post requires the root seat to act. Worker
// copies remain quiet unless marked. Human requests, Voice, alerts, failed
// reports and root-owned shell/workflow results keep their wake behavior.
func rootWakes(msg Message, roomName string) bool {
	if !msg.Addressed(roomName) && !msg.Addressed(roomName+":"+RootAgent) || msg.From == roomName+":"+RootAgent {
		return false
	}
	// A registered event follows its spec, so its family decides how it
	// reaches the root seat: a quiet one never wakes root, one that wakes its
	// target wakes root only when the post needs root, and an alert wakes it
	// now.
	if spec, ok := eventSpecFor(msg.Kind, msg.Notice.Kind); ok {
		switch spec.Wake {
		case WakeNever:
			return false
		case WakeRoot:
			return true
		}
		return msg.NeedsRoot
	}
	switch msg.Kind {
	case "", "message":
		return msg.NeedsRoot || msg.Human || OwnerRequest(msg, roomName) || msg.Source == SourceVoice
	// TODO: legacy/shim remove later — a post stored before its family was
	// typed still reads by its old kind. Remove when retained history ages out.
	case "notice":
		return msg.NeedsRoot || msg.Human || OwnerRequest(msg, roomName) || msg.Source == SourceVoice
	case "alert", "service-alert":
		return true
	case "seat", "pr", "merge":
		return msg.NeedsRoot
	case "completed":
		return msg.OwnerReport && (strings.HasPrefix(msg.From, "shell:") || strings.HasPrefix(msg.From, "run:"))
	case "failed", "stopped":
		return msg.OwnerReport
	}
	return false
}
