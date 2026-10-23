package room

import "testing"

const (
	ownerSession = "ses_20261023-120000-aaaaaaaa"
	otherSession = "ses_20261023-120001-bbbbbbbb"
)

// The writer's own conversation stands as written: the DM composer says where
// the post was written, and the store does not guess from the mentions.
func TestHomeForKeepsTheWriterConversation(t *testing.T) {
	msg := Message{From: "alpha", Human: true, To: []string{"alpha:lead"}, Conversation: DMConversation(ownerSession)}
	if got := HomeFor(msg, "alpha", ""); got != DMConversation(ownerSession) {
		t.Fatalf("home = %q, want the DM the writer named", got)
	}
	msg = Message{From: "alpha:lead", To: []string{"alpha"}, Conversation: ConversationVoice}
	if got := HomeFor(msg, "alpha", ""); got != ConversationVoice {
		t.Fatalf("home = %q, want voice", got)
	}
}

// A post of the owner's voice call is in the voice conversation, whatever it
// mentions.
func TestHomeForVoiceCall(t *testing.T) {
	msg := Message{From: "alpha:voice-call", Source: SourceVoice, To: []string{"alpha:lead"}, Wake: true}
	if got := HomeFor(msg, "alpha", ""); got != ConversationVoice {
		t.Fatalf("home = %q, want voice", got)
	}
	legacy := Message{From: "dyna", Kind: "voice", To: []string{"alpha"}}
	if got := HomeFor(legacy, "alpha", ""); got != ConversationVoice {
		t.Fatalf("legacy voice home = %q, want voice", got)
	}
}

// TODO: legacy/shim remove when the DM composer sends its conversation — an
// owner post the human typed to exactly one session is that session's DM,
// and an owner post to anything else stays in the room feed.
func TestHomeForOwnerPostToExactlyOneSession(t *testing.T) {
	bound := map[string]string{"alpha:lead": ownerSession}
	msg := Message{From: "alpha", Human: true, To: []string{"alpha:lead"}, SessionIDs: bound}
	if got := HomeFor(msg, "alpha", ""); got != DMConversation(ownerSession) {
		t.Fatalf("home = %q, want dm:%s", got, ownerSession)
	}
	for name, other := range map[string]Message{
		"two recipients": {From: "alpha", Human: true, To: []string{"alpha:lead", "alpha:root"}, SessionIDs: bound},
		"no session":     {From: "alpha", Human: true, To: []string{"alpha:lead"}},
		"root post":      {From: "alpha", Human: false, To: []string{"alpha:lead"}, SessionIDs: bound},
		"session author": {From: "alpha:lead", To: []string{"alpha:root"}, SessionIDs: bound},
	} {
		if got := HomeFor(other, "alpha", ""); got != ConversationRoom {
			t.Fatalf("%s: home = %q, want the room feed", name, got)
		}
	}
}

// A session that answers a post in its own DM stays in that DM; its other
// posts go to the room feed.
func TestHomeForSessionAnsweringItsOwnDM(t *testing.T) {
	own := map[string]string{"alpha:lead": ownerSession}
	msg := Message{From: "alpha:lead", To: []string{"alpha"}, SessionIDs: own, ReplyTo: "msg_parent"}
	if got := HomeFor(msg, "alpha", DMConversation(ownerSession)); got != DMConversation(ownerSession) {
		t.Fatalf("home = %q, want dm:%s", got, ownerSession)
	}
	if got := HomeFor(msg, "alpha", ConversationRoom); got != ConversationRoom {
		t.Fatalf("home = %q, want the room feed", got)
	}
	// A reply in another session's DM is not this session's home.
	if got := HomeFor(msg, "alpha", DMConversation(otherSession)); got != ConversationRoom {
		t.Fatalf("home = %q, want the room feed", got)
	}
}

// Everything else, including a post from before the field existed, is the
// room feed.
func TestHomeForDefaultsToTheRoom(t *testing.T) {
	if got := HomeFor(Message{From: "dyna", Kind: "notice", To: []string{"alpha"}}, "alpha", ""); got != ConversationRoom {
		t.Fatalf("home = %q, want the room feed", got)
	}
	if got := (Conversation("")).Home(); got != ConversationRoom {
		t.Fatalf("empty home = %q, want the room feed", got)
	}
}

func TestConversationValidate(t *testing.T) {
	for _, ok := range []Conversation{"", ConversationRoom, ConversationVoice, DMConversation(ownerSession)} {
		if err := ok.Validate(); err != nil {
			t.Fatalf("%q: %v", string(ok), err)
		}
	}
	for _, bad := range []Conversation{"dm:", "dm:alpha:lead", "feed", "dm:ses_a dm:ses_b"} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("%q was accepted", string(bad))
		}
	}
}
