package room

import "testing"

// The owner's voice call stays out of the feed and never wakes root; no
// session can take Voice's address.
func TestVoiceCallPostsStayInTheCall(t *testing.T) {
	owner := Message{From: "r", Human: true, Kind: "voice", Source: SourceVoice, Spoken: true, To: []string{"r:" + VoiceAgent}, Text: "check ci"}
	voice := Message{From: "r:" + VoiceAgent, Kind: "voice", Source: SourceVoice, Spoken: true, To: []string{"r"}, Text: "On it."}
	for _, msg := range []Message{owner, voice} {
		if WakesRoot(msg, "r") || DefaultView(msg, "r") {
			t.Fatalf("a call post woke root or showed in the feed: %+v", msg)
		}
	}
	if !ReservedName(VoiceAgent) || ValidateAgent(VoiceAgent) == nil {
		t.Fatal("a session could take Voice's name")
	}
}
