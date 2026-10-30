package room

import "testing"

func TestLegacyHumanSenderIsOwner(t *testing.T) {
	m := Message{From: "owner", Human: true}
	if got := m.SenderKindFor("qa-demo"); got != SenderOwner {
		t.Fatalf("human sender kind = %q, want owner", got)
	}
}
