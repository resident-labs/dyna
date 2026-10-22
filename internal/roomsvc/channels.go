package roomsvc

import (
	"slices"

	"dyna-agent/internal/room"
)

// channelIndex routes posts into the web app's channels: #general and one
// channel per lead. Channels group the room feed; they never change delivery
// or wakes. It uses the full stored member tree, including closed sessions.
type channelIndex struct {
	byAddress map[string]Member
	byID      map[string]Member
	owners    map[string]bool
}

// DMChannel holds posts that belong only in DM threads, such as the goal
// keeper's; no channel view shows it.
const DMChannel = "dm"

// VoiceChannel is the channel of the owner's voice call.
const VoiceChannel = room.VoiceAgent

func newChannelIndex(members []Member) channelIndex {
	c := channelIndex{map[string]Member{}, map[string]Member{}, map[string]bool{}}
	for _, m := range members {
		if m.Owner {
			c.owners[m.Address] = true
			continue
		}
		c.byAddress[m.Address], c.byID[m.ID] = m, m
	}
	return c
}

func (c channelIndex) home(m Member) string {
	if _, ok := c.byID[m.Parent]; ok {
		return "channel:" + m.Parent
	}
	return "general"
}

// views lists the channels a post shows in, the sender's home first.
func (c channelIndex) views(post room.Message) []string {
	// The owner's voice call is its own conversation, never #general. Root
	// still reads it: the call through its owner DM block, relays as input.
	if post.Source == room.SourceVoice {
		return []string{VoiceChannel}
	}
	// The goal keeper talks to a goal's holder, or to root, in their DM only.
	if post.From == room.GoalKeeperSender && post.Kind == "goal" {
		return []string{DMChannel}
	}
	sender, known := c.byAddress[post.From]
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
		// An explicit post to root shows in general.
		if c.owners[address] {
			add("general")
		}
	}
	// Unknown system posts stay visible in general rather than disappear.
	if len(out) == 0 {
		add("general")
	}
	return out
}
