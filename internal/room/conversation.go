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

// SeatDMConversation is the conversation of the owner's DM with the room's root
// seat. It names the seat, not the session that holds it today (see
// SeatSessionID), so the DM keeps one conversation when the holder changes: an
// external orchestrator's posts and the internal root's posts share it.
func SeatDMConversation(roomID string) Conversation {
	return DMConversation(SeatSessionID(roomID))
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
// it needs one well-formed session ID and nothing else; the root seat's DM
// names the seat of one room instead (see SeatDMConversation).
func (c Conversation) Validate() error {
	switch {
	case c == "" || c == ConversationRoom || c == ConversationVoice:
		return nil
	case c.IsDM() && (dmSessionRe.MatchString(c.SessionID()) || seatSessionRe.MatchString(c.SessionID())):
		return nil
	}
	return fmt.Errorf("conversation %q is not room, voice, dm:<session id> or dm:seat:<room id>", string(c))
}

// HomeFor decides the conversation one post is stored in. Every post gets
// exactly one home, and it never changes after the post is stored.
//
// The cases, in order:
//
//  1. A client that knows where it writes supplied a conversation
//     (Message.Conversation). The owner's DM composer names the DM it writes
//     in, dm:<session id> or the root seat's DM (see SeatDMConversation), and
//     an owner post from the room feed names nobody. What the writer says
//     stands as written, and the store never guesses a DM from the mentions:
//     a post the owner writes in the room feed to one session stays in the
//     feed, and that session's DM shows a reference row for it.
//  2. A post of the owner's voice call. SourceVoice marks the transcript
//     lines of the call, and the legacy "voice" kind marks the rest.
//  3. A typed event whose audience is one session belongs to that session's
//     DM: a parent's check-in is meant for the parent alone, not for the room
//     feed. A check-in sent to the root seat is the seat's DM, because every
//     root view reads dm:seat:<room id> whatever session holds the seat. Every
//     recipient must be that one session, or the post stays in the feed.
//  4. A session that answers a post in its own DM stays in that DM. parent is
//     the home of the post it replies to, so a session's other posts go to
//     the room feed and reach its DM as reference rows.
//  5. Everything else is the room feed.
//
// parent is the home of msg.ReplyTo (see Store.ReplyHome), and empty when the
// post answers nothing. msg.RoomID names the room of the seat's DM; a caller
// that stores a post always sets it.
func HomeFor(msg Message, roomName string, parent Conversation) Conversation {
	if msg.Conversation != "" {
		return msg.Conversation.Home()
	}
	if msg.Source == SourceVoice || msg.Kind == "voice" {
		return ConversationVoice
	}
	if msg.EventAudience() == AudienceSession {
		if id, addr := onlySessionRecipient(msg); id != "" {
			if addr == roomName+":"+RootAgent && msg.RoomID != "" {
				return SeatDMConversation(msg.RoomID)
			}
			return DMConversation(id)
		}
	}
	if id := msg.SessionIDs[msg.From]; id != "" && parent.Home() == DMConversation(id) {
		return DMConversation(id)
	}
	return ConversationRoom
}

// onlySessionRecipient is the one session msg is addressed to, with the address
// that names it, and empty when msg goes to nobody, to several sessions, or to
// anything else as well.
func onlySessionRecipient(msg Message) (id, addr string) {
	if len(msg.To) == 0 {
		return "", ""
	}
	for _, to := range msg.To {
		bound := msg.SessionIDs[to]
		if bound == "" || id != "" && bound != id {
			return "", ""
		}
		id, addr = bound, to
	}
	return id, addr
}
