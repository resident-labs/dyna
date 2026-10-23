package subagent

import (
	"fmt"
	"strings"

	"dyna-agent/internal/room"
)

// The internal root's quiet sections all render from the seat's stored quiet
// delivery rows. Their category picks the section: the owner's DMs to agents,
// and every other quiet post addressed to the root seat, whatever its kind.

// ownerDMNote lists the owner's DMs to agents in msgs, one line each with the
// start of the text and the post ID, and the IDs of those agents' later
// quiet reports to the owner. The internal root reads it so it knows what the
// owner approved in DMs. The owner-Voice call lines are not DMs to root and
// stay out. It is empty when msgs hold no owner DM.
func ownerDMNote(roomName string, msgs []room.Message, seats map[string]room.Delivery) string {
	var lines []string
	dmd := map[string]bool{}
	replies := map[string][]string{}
	var order []string
	for _, msg := range msgs {
		// A standing-instruction note belongs to the one session it names, and
		// one edit reaches every session. The folded line names the change once.
		if _, instruction := msg.InstructionNote(); instruction {
			continue
		}
		seat, ok := seats[msg.ID]
		if ok && seat.Category == room.RootOwnerDM {
			text := strings.Join(strings.Fields(msg.Text), " ")
			if r := []rune(text); len(r) > 200 {
				text = string(r[:200]) + "…"
			}
			names := make([]string, len(msg.To))
			for i, to := range msg.To {
				names[i] = "@" + strings.TrimPrefix(to, roomName+":")
				if !dmd[to] {
					dmd[to] = true
					order = append(order, to)
				}
			}
			lines = append(lines, fmt.Sprintf("- %s: %s (%s)", strings.Join(names, " "), text, msg.ID))
			continue
		}
		if ok && seat.Category == room.RootQuietPost && dmd[msg.From] && msg.Addressed(roomName) {
			replies[msg.From] = append(replies[msg.From], msg.ID)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	for _, from := range order {
		if ids := replies[from]; len(ids) > 0 {
			lines = append(lines, fmt.Sprintf("- @%s replied to the owner: %s", strings.TrimPrefix(from, roomName+":"), strings.Join(ids, ", ")))
		}
	}
	return fmt.Sprintf("[OWNER DMS] Owner DMs since your last turn. They did not wake you. Read one in full with `dyna room read %s <post-id>`.\n%s", roomName, strings.Join(lines, "\n"))
}

// quietRootPosts lists quiet posts addressed to the root seat once, with the
// next real root input, whatever their kind: issues, completions and notices
// all reach root. Waking posts are turn input, and the other categories have
// their own sections, so none of those appear here. Full posts remain in the
// room log.
func quietRootPosts(roomName string, msgs []room.Message, seats map[string]room.Delivery) string {
	var entries []string
	for _, msg := range msgs {
		// The folded instruction line is the one place a standing-instruction
		// note reaches the root seat.
		if _, instruction := msg.InstructionNote(); instruction {
			continue
		}
		if seat, ok := seats[msg.ID]; ok && seat.Category == room.RootQuietPost {
			text := strings.Join(strings.Fields(msg.Text), " ")
			if r := []rune(text); len(r) > 240 {
				text = string(r[:240]) + "…"
			}
			entries = append(entries, fmt.Sprintf("- %s: %s (%s)", msg.From, text, msg.ID))
		}
	}
	if len(entries) == 0 {
		return ""
	}
	const limit = 30
	omitted := ""
	if len(entries) > limit {
		omitted = fmt.Sprintf("%d older quiet posts omitted; use `dyna room log %s --all` to read them.\n", len(entries)-limit, roomName)
		entries = entries[len(entries)-limit:]
	}
	return fmt.Sprintf("[QUIET ROOT POSTS] Information since your last turn. These posts did not wake you. Read full posts with `dyna room read %s <post-id>`.\n%s", roomName, omitted+strings.Join(entries, "\n"))
}

// instructionWindowMS is how long one instruction change lasts: its notes reach
// every session at once, so they arrive within a minute of each other.
const instructionWindowMS int64 = 60_000

// instructionLines folds the standing-instruction notes in msgs into one line
// per change, such as "Instructions changed for 35 sessions: Subagent
// profiles". One edit reaches every session, so a burst is one fact, not one
// fact per session. A change is one note per session of the same sections
// within a minute of its first note: a session that gets a second note of the
// same sections got a later edit, which is its own change. Every line counts
// the distinct sessions it names.
func instructionLines(msgs []room.Message) []string {
	type change struct {
		sections []string
		firstMS  int64
		sessions map[string]bool
	}
	var order []*change
	current := map[string]*change{}
	for _, msg := range msgs {
		ev, ok := msg.InstructionNote()
		if !ok {
			continue
		}
		names := make([]string, 0, len(ev.Sections))
		for _, section := range ev.Sections {
			if section.Name != "" {
				names = append(names, section.Name)
			}
		}
		fingerprint := strings.Join(names, "\x00")
		found := current[fingerprint]
		if found == nil || found.sessions[ev.Session] || msg.TS-found.firstMS >= instructionWindowMS || msg.TS < found.firstMS {
			found = &change{sections: names, firstMS: msg.TS, sessions: map[string]bool{}}
			current[fingerprint] = found
			order = append(order, found)
		}
		found.sessions[ev.Session] = true
	}
	lines := make([]string, len(order))
	for i, one := range order {
		sections, sessions := "their standing instructions", fmt.Sprintf("%d sessions", len(one.sessions))
		if len(one.sessions) == 1 {
			sessions = "1 session"
		}
		if len(one.sections) > 0 {
			sections = strings.Join(one.sections, ", ")
		}
		lines[i] = fmt.Sprintf("- Instructions changed for %s: %s.", sessions, sections)
	}
	return lines
}

// seatNotes renders the root's quiet sections for msgs from the seat's stored
// delivery rows. A read that fails leaves the sections out: they are context,
// and the posts themselves stay in the room log.
func seatNotes(rooms *room.Store, meta Meta, msgs []room.Message) []string {
	ids := make([]string, len(msgs))
	for i, msg := range msgs {
		ids[i] = msg.ID
	}
	seats, err := rooms.SeatDeliveries(meta.RoomID, ids)
	if err != nil {
		return nil
	}
	var out []string
	for _, note := range []string{ownerDMNote(meta.Room, msgs, seats), quietRootPosts(meta.Room, msgs, seats)} {
		if note != "" {
			out = append(out, note)
		}
	}
	if lines := instructionLines(msgs); len(lines) > 0 {
		out = append(out, fmt.Sprintf("[INSTRUCTIONS CHANGED] One instruction edit reaches every session, so each change is one line here. Each session reads its own note.\n%s", strings.Join(lines, "\n")))
	}
	return out
}
