package harness

import (
	"context"
	"testing"

	"dyna-agent/internal/profile"
)

// A turn that ends cleanly with no text, and owes no report, is finished. A
// turn that ends in an error, without a final message, or that owes a report
// still fails.
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
		owes    bool
		wantErr bool
	}{
		{"pi clean stop", profile.HarnessPi, piStop, false, false},
		{"pi clean stop owing a report", profile.HarnessPi, piStop, true, true},
		{"pi error stop", profile.HarnessPi, piError, false, true},
		{"pi no final message", profile.HarnessPi, piNoFinal, false, true},
		{"pi no events", profile.HarnessPi, "", false, true},
		{"claude clean result", profile.HarnessClaudeCode, claudeOK, false, false},
		{"claude clean result owing a report", profile.HarnessClaudeCode, claudeOK, true, true},
		{"claude error result", profile.HarnessClaudeCode, claudeErr, false, true},
		{"claude no result", profile.HarnessClaudeCode, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &journaledRun{
				ctx:  context.Background(),
				p:    profile.Profile{Name: "w", Harness: tc.harness},
				spec: commandSpec{argv: []string{"worker"}},
				exec: ExecutionOptions{AcceptCleanEmptyStop: func() bool { return !tc.owes }},
			}
			err := r.attemptFailure(attempt{stdout: tc.stdout, started: true})
			if (err != nil) != tc.wantErr {
				t.Fatalf("attemptFailure() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
