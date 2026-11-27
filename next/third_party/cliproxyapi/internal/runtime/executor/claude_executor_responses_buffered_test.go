package executor

import (
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

func TestBufferedClaudeResponsesFailureKeepsObservedUsage(t *testing.T) {
	const start = `data: {"type":"message_start","message":{"id":"msg_fixture","model":"claude-fixture","usage":{"input_tokens":12}}}` + "\n\n"
	for _, tc := range []struct {
		name, frame, message string
		truncated            bool
	}{
		{"provider error", `data: {"type":"error","error":{"type":"overloaded_error","message":"fixture error","metadata":"not public"}}`, "claude executor: upstream returned error event: fixture error", false},
		{"missing delta", `data: {"type":"message_stop"}` + "\n\n", "claude executor: upstream stream response ended before message completion", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage helps.StreamUsageBuffer
			_, err := readBufferedClaudeResponses([]byte(start+tc.frame), nil, &usage)
			var status statusErr
			if !errors.As(err, &status) || status.StatusCode() != http.StatusBadGateway || status.IsStreamTruncated() != tc.truncated || status.Error() != tc.message {
				t.Fatalf("failure = %T %v, want typed 502 (truncated=%t) %q", err, err, tc.truncated, tc.message)
			}
			if detail, ok := usage.Detail(); !ok || detail.InputTokens != 12 {
				t.Fatalf("usage = %+v, present=%t; want 12 input tokens", detail, ok)
			}
		})
	}
}
