package room

import (
	"fmt"
	"regexp"
	"strings"
)

// Conversation names where one post was written: the room feed, the owner's
// DM with one session, or the owner's voice call. A post keeps the
// conversation it was written in for good. Every other place it reaches shows
// a reference to it, never a second copy.
type Conversation string

const (
	// ConversationRoom is the room feed, where every member reads every post.
	// The empty value means the same, so a stored post from before this field
	// existed reads as the room feed.
	ConversationRoom Conversation = "room"
	// ConversationVoice is the owner's voice call with the room.
	ConversationVoice Conversation = "voice"
)

// dmPrefix starts a DM conversation: dm:<sessionID>.
const dmPrefix = "dm:"

// dmSessionRe is the shape of the one session a DM names.
var dmSessionRe = regexp.MustCompile(`^ses_[A-Za-z0-9_.-]{1,96}$`)

// DMConversation is the conversation of the owner's DM with sessionID.
func DMConversation(sessionID string) Conversation {
	return Conversation(dmPrefix + sessionID)
}

// Home is c itself, with the empty value read as the room feed.
func (c Conversation) Home() Conversation {
	if c == "" {
		return ConversationRoom
	}
	return c
}

// String renders c for storage and logs; the empty value renders as the room feed.
func (c Conversation) String() string { return string(c.Home()) }

// IsDM reports whether c is the owner's DM with one session.
func (c Conversation) IsDM() bool { return strings.HasPrefix(string(c), dmPrefix) }

// SessionID is the session of a DM conversation, and empty for the room feed
// and the voice call.
func (c Conversation) SessionID() string {
	if !c.IsDM() {
		return ""
	}
	return strings.TrimPrefix(string(c), dmPrefix)
}

// Validate reports whether c names a conversation the store can keep. The
// empty value is the room feed. A DM is the owner and exactly one session, so
// it needs one well-formed session ID and nothing else.
func (c Conversation) Validate() error {
	switch {
	case c == "" || c == ConversationRoom || c == ConversationVoice:
		return nil
	case c.IsDM() && dmSessionRe.MatchString(c.SessionID()):
		return nil
	}
	return fmt.Errorf("conversation %q is not room, voice or dm:<session id>", string(c))
}

// HomeFor decides the conversation one post is stored in. Every post gets
// exactly one home, and it never changes after the post is stored.
//
// The cases, in order:
//
//  1. The writer supplied a conversation (Message.Conversation). The owner's
//     DM composer and any other client that knows where it writes set it, and
//     it stands as written.
//  2. A post of the owner's voice call. SourceVoice marks the transcript
//     lines of the call, and the legacy "voice" kind marks the rest.
//  3. TODO: legacy/shim remove when the DM composer sends its conversation —
//     an owner post that the human typed to exactly one session is that
//     session's DM. Until PR 2 the composer cannot say where a post was
//     written, so the store reads it from the one bound recipient. An owner
//     post to the room, to several sessions, or to a recipient without a
//     session binding stays in the feed.
//  4. A session that answers a post in its own DM stays in that DM. parent is
//     the home of the post it replies to, so a session's other posts go to
//     the room feed and reach its DM as reference rows.
//  5. Everything else is the room feed.
//
// parent is the home of msg.ReplyTo (see Store.ReplyHome), and empty when the
// post answers nothing.
func HomeFor(msg Message, roomName string, parent Conversation) Conversation {
	if msg.Conversation != "" {
		return msg.Conversation.Home()
	}
	if msg.Source == SourceVoice || msg.Kind == "voice" {
		return ConversationVoice
	}
	if msg.Human && msg.From == roomName && len(msg.To) == 1 {
		if id := msg.SessionIDs[msg.To[0]]; id != "" {
			return DMConversation(id)
		}
	}
	if id := msg.SessionIDs[msg.From]; id != "" && parent.Home() == DMConversation(id) {
		return DMConversation(id)
	}
	return ConversationRoom
}
