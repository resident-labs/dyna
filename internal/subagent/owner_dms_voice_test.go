package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// The owner-Voice call lines stay out of root's [OWNER DMS] block; an
// ordinary owner DM in the same span still shows.
func TestOwnerDMNoteLeavesOutTheVoiceCall(t *testing.T) {
	msgs := []room.Message{
		{ID: "m1", From: "r", Human: true, Kind: "voice", Source: room.SourceVoice, Spoken: true, To: []string{"r:" + room.VoiceAgent}, Text: "what is the merge queue doing"},
		{ID: "m2", From: "r:" + room.VoiceAgent, Kind: "voice", Source: room.SourceVoice, Spoken: true, To: []string{"r"}, Text: "Let me ask root."},
		{ID: "m3", From: "r", Human: true, To: []string{"r:worker"}, Text: "Ship it."},
	}
	note := ownerDMNote("r", msgs, seatRowsFor("r", msgs))
	if !strings.Contains(note, "@worker: Ship it. (m3)") {
		t.Fatalf("note lacks the owner's DM:\n%s", note)
	}
	for _, id := range []string{"m1", "m2"} {
		if strings.Contains(note, id) {
			t.Fatalf("note lists call line %s:\n%s", id, note)
		}
	}
}
