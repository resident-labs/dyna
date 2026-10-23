package notify

import (
	"testing"

	"dyna-agent/internal/room"
)

// Only the owner's own client says where it writes. A session that names
// another session's DM or the voice call still lands in the room feed, and
// the owner's client keeps the conversation it names.
func TestOnlyTheOwnerChoosesTheConversation(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	for _, conversation := range []room.Conversation{room.DMConversation(parent.ID), room.ConversationVoice} {
		res, err := e.svc.Post(e.ctx, PostInput{
			Sender:       Sender{Kind: SenderSession, Room: rec.Name, Session: child},
			Mentions:     []string{"@" + rec.Name},
			Text:         "hello",
			Conversation: conversation,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Message.Conversation != room.ConversationRoom {
			t.Fatalf("a session post that names %q was stored in %q", conversation, res.Message.Conversation)
		}
	}
	want := room.DMConversation(parent.ID)
	res, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: rec.Name}, Mentions: []string{"@" + rec.Name}, Text: "hi", Conversation: want})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Conversation != want {
		t.Fatalf("the owner's post was stored in %q, want %q", res.Message.Conversation, want)
	}
}
