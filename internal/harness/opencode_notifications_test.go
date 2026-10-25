package harness

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/providerfailure"
)

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
		{"payment required", 402, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{
				"sessionID": "session-1",
				"error":     map[string]any{"type": tc.kind, "message": "the provider said no", "status": tc.status},
			})
			if err != nil {
				t.Fatal(err)
			}
			n := openCodeTurnNotifications{session: "session-1", pending: map[string]bool{}}
			n.observe(openCodeEvent{Type: "session.execution.failed", Data: data})
			reason, ok := providerfailure.ReasonOf(n.err)
			if ok != (tc.want != "") || reason != tc.want {
				t.Fatalf("reason = %q, %v, want %q (error %v)", reason, ok, tc.want, n.err)
			}
		})
	}
}
