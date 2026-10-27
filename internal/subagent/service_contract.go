package subagent

import (
	"errors"
	"strings"

	"dyna-agent/internal/room"
)

// ServiceContract is the shared output policy for managed sessions and their crew.
// It is computed from session metadata, never stored as another classification.
type ServiceContract struct{ session Meta }

func (m Meta) ServiceContract() ServiceContract { return ServiceContract{session: m} }

// Active reports whether the service output policy applies. The internal
// root is a service session only for group posts and goals: it answers the
// owner like any session, so its posts and final responses reach the room.
func (c ServiceContract) Active() bool { return c.session.Service && c.session.Role != RoleRoot }
func (c ServiceContract) Helper() bool {
	return c.Active() && c.session.Role == "" && c.session.ParentSession != ""
}

func (c ServiceContract) CrewTarget(target Meta) bool {
	return c.Active() && target.ID != "" && target.RoomID == c.session.RoomID &&
		((c.session.ParentSession != "" && target.ID == c.session.ParentSession) || target.ParentSession == c.session.ID)
}

// DirectMention reports whether post names this session directly. The session
// must be a recipient bound by ID, and a mention must name that binding. A
// group selector such as @team does not count.
func (c ServiceContract) DirectMention(post room.Message) bool {
	var keys []string
	for _, to := range post.To {
		if post.SessionIDs[to] == c.session.ID {
			keys = append(keys, to)
		}
	}
	for _, mention := range post.Via {
		if mention == "@parent" || strings.HasSuffix(mention, ":parent") {
			return len(keys) > 0
		}
		for _, key := range keys {
			if mention == "@"+key || mention == "@"+strings.TrimPrefix(key, c.session.Room+":") {
				return true
			}
		}
	}
	return false
}

// FrameReply gives services the same reply rights that post validation enforces.
func (c ServiceContract) FrameReply(post room.Message, fallback string) (string, string) {
	if !c.Active() {
		return fallback, ""
	}
	if c.DirectMention(post) {
		return fallback, post.ID
	}
	return "", ""
}

var ErrServiceOutput = errors.New("service sessions may post only crew work to their actual parent or children, or a --reply-to response only to a direct mention's sender; general posts and dyna request question are not allowed")

func (c ServiceContract) Prompt() string {
	if !c.Active() {
		return ""
	}
	return `[SERVICE SESSION CONTRACT]
You are a service session. Your work product is the assigned artifact: a recorded review verdict or helper findings sent to your actual parent. Return the required result to the harness. Dyna sends a helper's final findings to its current parent automatically. Reviewer final responses stay in the session transcript. Helpers can send earlier findings with dyna post @parent. Do not post the same final findings twice.
Do not post status messages or general chat. You may post crew findings to your actual parent and assignments to your own children. Reply to a direct mention only to its sender, with dyna post --reply-to <post-id>. A group post does not grant reply rights. Use dyna post @root --alert only for a missing profile or a failed dependency that requires owner action. An alert is not a status update. A helper sends alerts only to its parent. Identical alerts are delivered at most once per hour. Do not use dyna request question. A refused post names the allowed outputs; do not retry the same refused post.
[/SERVICE SESSION CONTRACT]`
}
