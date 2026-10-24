package roomsvc

import (
	"slices"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// feedViews routes posts into the web app's feed views: the room chat and one
// view per lead. A feed view filters the room feed; it never changes delivery
// or wakes. It uses the full stored member tree, including closed sessions.
type feedViews struct {
	byAddress map[string]Member
	byID      map[string]Member
	owners    map[string]bool
	// root is the room's root session. Its team is the room itself, so it has
	// no team view of its own.
	root Member
}

// VoiceChannel is the feed view of the owner's voice call. It is a view token,
// not a conversation: the call's posts have the voice conversation as their
// home, and the call view lists them there.
const VoiceChannel = room.VoiceAgent

func newFeedViews(members []Member) feedViews {
	c := feedViews{byAddress: map[string]Member{}, byID: map[string]Member{}, owners: map[string]bool{}}
	for _, m := range members {
		if m.Owner {
			c.owners[m.Address] = true
			continue
		}
		if m.Role == subagent.RoleRoot {
			c.root = m
		}
		c.byAddress[m.Address], c.byID[m.ID] = m, m
	}
	return c
}

// home is the view token a member's posts belong to. The token keeps its
// "channel:" prefix because routes and saved views store it, so a rename would
// break links the owner already has.
//
// The root session's team is the whole room: its children are the room's
// top-level sessions. Root's own posts, and the posts of its children, belong
// to the room chat, so the owner reads them there and not in a team view.
//
// TODO: legacy/shim remove later — change the token only together with a read
// for the "channel:" form.
func (c feedViews) home(m Member) string {
	if m.Role == subagent.RoleRoot {
		return "general"
	}
	if parent, ok := c.byID[m.Parent]; ok && parent.Role != subagent.RoleRoot {
		return "channel:" + m.Parent
	}
	return "general"
}

// addressedToRoot reports a post whose recipients include the room's root
// session, which the owner reads as a post to them.
func (c feedViews) addressedToRoot(post room.Message) bool {
	return c.root.Address != "" && slices.Contains(post.To, c.root.Address)
}

// viewsFor lists the feed views a post shows in, the sender's home first. home
// is the conversation the post was written in: a post of the owner's voice call
// shows in the call view only, and a post written in a DM shows in no feed view,
// because its DM lists it.
func (c feedViews) viewsFor(post room.Message, home room.Conversation) []string {
	// The owner's voice call is its own conversation, never the room chat. Root
	// still reads it: the call through its owner DM block, relays as input.
	if home == room.ConversationVoice {
		return []string{VoiceChannel}
	}
	if home.IsDM() {
		return nil
	}
	sender, known := c.byAddress[post.From]
	// A typed turn event names the session whose turn ended, which the engine
	// posted on its behalf, so the post stays in that session's team view.
	if ev, ok := post.TurnResult(); ok {
		sender, known = c.byAddress[ev.Session]
	}
	// Automatic results stay with the sender's peers, despite delivery to root.
	if IsCompletion(post) {
		return []string{c.home(sender)}
	}
	childrenOnly := known && len(post.To) > 0
	for _, address := range post.To {
		m, ok := c.byAddress[address]
		childrenOnly = childrenOnly && ok && m.Parent == sender.ID
	}
	var out []string
	add := func(view string) {
		if !slices.Contains(out, view) {
			out = append(out, view)
		}
	}
	if known && !childrenOnly {
		add(c.home(sender))
	}
	for _, address := range post.To {
		if recipient, ok := c.byAddress[address]; ok {
			// A message to the sender's own parent belongs to that parent's team.
			if known && sender.Parent == recipient.ID {
				continue
			}
			add(c.home(recipient))
		}
		// An explicit post to root shows in the room chat.
		if c.owners[address] {
			add("general")
		}
	}
	// Unknown system posts stay visible in the room chat rather than disappear.
	if len(out) == 0 {
		add("general")
	}
	return out
}
