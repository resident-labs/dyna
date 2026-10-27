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

// Root categories name the quiet sections both roots render from the seat's
// quiet deliveries.
const (
	// RootOwnerDM is the owner's post to an agent, not to the root seat.
	RootOwnerDM = "owner-dm"
	// RootQuietPost is any other quiet post addressed to the root seat,
	// whatever its kind: issues, completions and notices all reach root.
	RootQuietPost = "quiet-post"
)

// rootSelfPost reports a post the root seat itself wrote: an agent post from
// the seat's address, or a typed turn end that names the seat's session, which
// the engine posts on that session's behalf.
func rootSelfPost(msg Message, roomName string) bool {
	if ev, ok := msg.TurnResult(); ok {
		return ev.Session == roomName+":"+RootAgent || ev.Session == roomName
	}
	return authoredByRootSeat(msg, roomName)
}

// authoredByRootSeat reports a post written by this room's root seat.
func authoredByRootSeat(msg Message, roomName string) bool {
	return msg.Sender() == SenderRoot && msg.From == roomName+":"+RootAgent
}

// RootContext classifies one post for the root seat: wake, quiet or skip,
// plus the quiet post's category. It is the one write-time classifier: notify
// stores each post's seat delivery row from it, and every reader reads that
// row instead of classifying the post again. The root's own posts and the
// Voice call's transcript lines are never root context, and the owner's posts
// to agents stay quiet: the agents answer them, and root only learns what was
// said. A session of another room that posts to the seat asks it to act, so
// that post wakes. Wake intent is the post's own Wake field; passive turn
// input has no quiet category, because the internal root reads it in full.
func RootContext(msg Message, roomName string) (RootDecision, string) {
	// The Voice call's lines stay in the call, and the seat's own posts never
	// come back to it. An orchestrator's own post as the owner is an echo too,
	// with two exceptions: a post that needs root, and one addressed to the root
	// session itself, which the owner's shell and API posts reach directly.
	if msg.Kind == "voice" || rootSelfPost(msg, roomName) {
		return RootSkip, ""
	}
	if msg.From == roomName && !msg.Human && !msg.EngineEvent() && !msg.Addressed(roomName+":"+RootAgent) && !msg.NeedsRoot && !ownerNamesRoot(msg, roomName) {
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
	return rootWakes(msg, roomName) || msg.Addressed(roomName+":"+RootAgent) && (msg.From == roomName || msg.From == EngineSender || msg.Kind == string(FamilyCheckin))
}

// rootWakes reports whether a post requires the root seat to act. A session's
// message to the seat wakes it: an agent's question to its root parent must
// not wait in the quiet digest. Human requests, Voice, alerts, failed reports
// and root-owned shell/workflow results keep their wake behavior.
func rootWakes(msg Message, roomName string) bool {
	if !msg.Addressed(roomName) && !msg.Addressed(roomName+":"+RootAgent) || authoredByRootSeat(msg, roomName) {
		return false
	}
	// A registered event follows its spec, so its family decides how it
	// reaches the root seat: a quiet one never wakes root, one that wakes its
	// target wakes root only when the post needs root, and an alert wakes it
	// now. A result root asked for itself wakes it when the owner is owed it.
	if spec, ok := eventSpecFor(msg.Kind, msg.Notice.Kind); ok {
		if msg.OwnerReport && spec.RootOnOwnerReport {
			return true
		}
		switch spec.Wake {
		case WakeNever:
			return false
		case WakeRoot:
			return true
		}
		return msg.NeedsRoot
	}
	// A post from a session of another room asks this seat to act. The sender
	// named this room's root because its own root does not read this room's
	// work, so a quiet copy would wait for a wake that never comes. The post
	// records the sender's own room when notify stores it, so a rename or a
	// display address cannot change the decision. The engine, a shell and a
	// workflow run record no sender room, so their posts keep the kind rules
	// below.
	if senderFromAnotherRoom(msg) {
		return msg.Wake
	}
	switch msg.Kind {
	case "", "message":
		// A session's message to the seat asks root to act: an agent's question
		// to its parent root must not wait in the quiet digest. notify binds a
		// session's own address to its session, so the engine, shells and
		// workflow runs keep the rules below.
		if msg.Sender() == SenderSession && msg.Wake {
			return true
		}
		// A quiet post never asks root to act, even when the owner's identity
		// names the seat. --quiet wins over that wake.
		return msg.NeedsRoot || msg.Human || OwnerRequest(msg, roomName) || msg.Source == SourceVoice || (ownerNamesRoot(msg, roomName) && msg.Wake)
	// TODO: legacy/shim remove later — a post stored before its family was
	// typed still reads by its old kind. Remove when retained history ages out.
	case "notice":
		return msg.NeedsRoot || msg.Human || OwnerRequest(msg, roomName) || msg.Source == SourceVoice
	case "alert", "service-alert":
		return true
	case "seat", "pr", "merge":
		return msg.NeedsRoot
	case "completed":
		return msg.OwnerReport && (msg.Sender() == SenderShell || msg.Sender() == SenderRun)
	case "failed", "stopped":
		return msg.OwnerReport
	}
	return false
}

// ownerNamesRoot reports a post the owner's identity addressed to the root
// seat by name: a script, CI hook or bridge that runs `dyna post @<room>:root`.
// A post that asks for a wake asks root to act, like the same post typed in
// the desktop. A quiet one still names the seat, so root reads it as context,
// but it does not ask root to act. Without the name, an owner-identity post
// is an orchestrator's echo and is not root context.
func ownerNamesRoot(msg Message, roomName string) bool {
	return msg.From == roomName && !msg.EngineEvent() && msg.RootSeatTarget(roomName)
}

// senderFromAnotherRoom reports a post whose sender is a session of a room
// other than this one. The post records the sender's room when it is stored,
// so the decision reads that identity instead of the display address.
func senderFromAnotherRoom(msg Message) bool {
	kind := msg.Sender()
	return (kind == SenderSession || kind == SenderRoot) && msg.SenderRoomID != "" && msg.SenderRoomID != msg.RoomID
}
