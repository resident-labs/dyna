package subagent

import (
	"fmt"
	"strings"

	"dyna-agent/internal/room"
)

// The internal root's quiet sections all render from room.RootContext, the
// one classifier both roots use. Its categories pick the section: owner DMs
// and every other quiet post addressed to the root seat, whatever its kind.

// ownerDMNote lists the owner's DMs to agents in msgs, one line each with the
// start of the text and the post ID, and the IDs of those agents' later
// quiet reports to the owner. The internal root reads it so it knows what the
// owner approved in DMs. The owner-Voice call lines are not DMs to root and
// stay out. It is empty when msgs hold no owner DM.
func ownerDMNote(roomName string, msgs []room.Message) string {
	var lines []string
	dmd := map[string]bool{}
	replies := map[string][]string{}
	var order []string
	for _, msg := range msgs {
		decision, category := room.RootContext(msg, roomName)
		if category == room.RootOwnerDM {
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
		if decision == room.RootQuiet &&
			category == room.RootQuietPost && dmd[msg.From] && msg.Addressed(roomName) {
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
func quietRootPosts(roomName string, msgs []room.Message) string {
	var entries []string
	for _, msg := range msgs {
		if decision, category := room.RootContext(msg, roomName); decision == room.RootQuiet && category == room.RootQuietPost {
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
