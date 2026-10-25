package harness

import (
	"context"
	"encoding/json"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/workeroutput"
)

// openCodeFailureEvent is the provider event a test feeds to the translator.
type openCodeFailureEvent struct {
	SessionID string `json:"sessionID"`
	Error     struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Status  int    `json:"status"`
	} `json:"error"`
}

// The provider's structured status and type name the reason of an OpenCode
// execution failure. Its message text is not evidence.
func TestOpenCodeExecutionFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		kind   string
		want   providerfailure.Reason
	}{
		{"unauthorized", 401, "", providerfailure.Auth},
		{"quota", 429, "", providerfailure.Quota},
		{"busy provider", 503, "", providerfailure.Capacity},
		{"quota type", 0, "provider.quota", providerfailure.Quota},
		{"rate limit type", 0, "rate-limit", providerfailure.Quota},
		// A 402 asks for payment, and its record still carries a quota type.
		{"payment required", 402, "provider.quota", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fixture openCodeFailureEvent
			fixture.SessionID = "session-1"
			fixture.Error.Type, fixture.Error.Message, fixture.Error.Status = tc.kind, "the provider said no", tc.status
			data, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			n := openCodeTurnNotifications{session: "session-1", pending: map[string]bool{}}
			lines := n.observe(openCodeEvent{Type: "session.execution.failed", Data: data})
			if reason, ok := providerfailure.ReasonOf(n.err); ok != (tc.want != "") || reason != tc.want {
				t.Fatalf("harness reason = %q, %v, want %q (error %v)", reason, ok, tc.want, n.err)
			}
			// The emitted record and the turn error must agree with the harness:
			// a record cannot turn a payment request back into a spent account.
			if len(lines) != 1 {
				t.Fatalf("translated lines = %v", lines)
			}
			if reason, ok := providerfailure.ReasonOf(workeroutput.OpenCodeError(lines[0])); ok != (tc.want != "") || reason != tc.want {
				t.Fatalf("record reason = %q, %v, want %q (record %s)", reason, ok, tc.want, lines[0])
			}
			turnErr := attemptError(context.Background(), profile.Profile{Name: "opencode", Harness: profile.HarnessOpenCode}, []string{"opencode"}, attempt{stdout: lines[0], stderr: ""})
			if reason, ok := providerfailure.ReasonOf(turnErr); ok != (tc.want != "") || reason != tc.want {
				t.Fatalf("turn reason = %q, %v, want %q (error %v)", reason, ok, tc.want, turnErr)
			}
		})
	}
}
