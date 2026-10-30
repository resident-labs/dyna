package room

import "testing"

func TestLegacyEngineForwardIsEngine(t *testing.T) {
	m := Message{From: EngineSender, Forward: &Forward{ID: "msg_1"}}
	for _, roomName := range []string{"cozycraft", EngineSender} {
		if got := m.SenderKindFor(roomName); got != SenderEngine {
			t.Fatalf("engine forward in %s = %q, want engine", roomName, got)
		}
	}
}

func TestLegacyHumanSenderIsOwner(t *testing.T) {
	m := Message{From: "owner", Human: true}
	if got := m.SenderKindFor("qa-demo"); got != SenderOwner {
		t.Fatalf("human sender kind = %q, want owner", got)
	}
}
