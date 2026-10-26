package harness

import (
	"context"
	"testing"

	"dyna-agent/internal/profile"
)

// A turn that ends cleanly with no text is finished, not a transient failure.
// A turn that ends in an error or without a final message still fails.
func TestEmptyOutputFailsOnlyWithoutACleanStop(t *testing.T) {
	const (
		piStop    = `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop"}}` + "\n" + `{"type":"agent_settled"}`
		piError   = `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"boom"}}`
		piNoFinal = `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"toolUse"}}`
		claudeOK  = `{"type":"result","subtype":"success","is_error":false,"result":""}`
		claudeErr = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":""}`
	)
	cases := []struct {
		name    string
		harness string
		stdout  string
		wantErr bool
	}{
		{"pi clean stop", profile.HarnessPi, piStop, false},
		{"pi error stop", profile.HarnessPi, piError, true},
		{"pi no final message", profile.HarnessPi, piNoFinal, true},
		{"pi no events", profile.HarnessPi, "", true},
		{"claude clean result", profile.HarnessClaudeCode, claudeOK, false},
		{"claude error result", profile.HarnessClaudeCode, claudeErr, true},
		{"claude no result", profile.HarnessClaudeCode, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := profile.Profile{Name: "w", Harness: tc.harness}
			err := attemptError(context.Background(), p, []string{"worker"}, attempt{stdout: tc.stdout, started: true})
			if (err != nil) != tc.wantErr {
				t.Fatalf("attemptError() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
