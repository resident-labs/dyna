package harness

import (
	"context"
	"testing"

	"dyna-agent/internal/profile"
)

// A turn that ends cleanly with no text is finished, whether or not it owes a
// report. A turn that ends in an error, without a final message, or that needs
// output still fails.
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
		accept  bool
		wantErr bool
	}{
		{"pi clean stop", profile.HarnessPi, piStop, true, false},
		{"pi clean stop needing output", profile.HarnessPi, piStop, false, true},
		{"pi error stop", profile.HarnessPi, piError, true, true},
		{"pi no final message", profile.HarnessPi, piNoFinal, true, true},
		{"pi no events", profile.HarnessPi, "", true, true},
		{"claude clean result", profile.HarnessClaudeCode, claudeOK, true, false},
		{"claude clean result needing output", profile.HarnessClaudeCode, claudeOK, false, true},
		{"claude error result", profile.HarnessClaudeCode, claudeErr, true, true},
		{"claude no result", profile.HarnessClaudeCode, "", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &journaledRun{
				ctx:  context.Background(),
				p:    profile.Profile{Name: "w", Harness: tc.harness},
				spec: commandSpec{argv: []string{"worker"}},
				exec: ExecutionOptions{AcceptCleanEmptyStop: tc.accept},
			}
			err := r.attemptFailure(attempt{stdout: tc.stdout, started: true})
			if (err != nil) != tc.wantErr {
				t.Fatalf("attemptFailure() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
