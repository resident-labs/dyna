package workflow

import "testing"

func TestWaitOnlyAgentPrompt(t *testing.T) {
	for _, tt := range []struct {
		prompt string
		want   bool
	}{
		{"Wait for file /tmp/ready", true},
		{"poll for file receipt.json", true},
		{"Wait for file /tmp/ready then review it", false},
		{"Review how to wait for file /tmp/ready", false},
		{"Wait for a decision from the reviewer", false},
	} {
		if got := waitOnlyAgentPrompt.MatchString(tt.prompt); got != tt.want {
			t.Errorf("prompt %q: got %v, want %v", tt.prompt, got, tt.want)
		}
	}
}
