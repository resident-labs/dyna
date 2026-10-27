package subagent

import "testing"

func TestLooksLikeDelivery(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"turn frame", "ok\n[DYNA SESSION — turn 143 in the same conversation.]\n[dyna message msg_4c from horizon:swarm-plan]\ngo", true},
		{"post envelope", "[dyna message msg_4c from a:b]\nhi", true},
		{"plain reply", "I read [dyna message msg_4c from a:b] in the log.", false},
		{"fenced", "Example:\n```\n[dyna message msg_4c from a:b]\n```", false},
		{"quote line", "> [dyna message msg_4c from a:b]", false},
		{"after quoted", "The post, quoted:\n[dyna message msg_4c from a:b]", false},
	}
	for _, c := range cases {
		if got := looksLikeDelivery(c.text); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
