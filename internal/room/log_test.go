package room

import "testing"

func TestAgentPost(t *testing.T) {
	for _, tc := range []struct {
		from, kind string
		want       bool
	}{
		{"alpha", "message", true}, {"alpha:lead", "message", true},
		{"alpha:lead", "completed", true}, {"alpha:lead", "failed", true},
		{"alpha:lead", "stopped", true}, {"alpha:lead", "", true},
		{"shell:42", "completed", false}, {"shell:42", "failed", false},
		{"run:42", "completed", false}, {"run:42/agent:1", "message", true},
		{"alpha:lead", "pr", false}, {"alpha", "spawn", false},
	} {
		if got := agentPost(Message{From: tc.from, Kind: tc.kind}, "alpha"); got != tc.want {
			t.Errorf("agentPost(%q, %q) = %v", tc.from, tc.kind, got)
		}
	}
}
