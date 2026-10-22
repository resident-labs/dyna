package workeroutput

import (
	"strings"
	"testing"
)

func TestOtherTerminalProviderErrorsIgnoreAssistantAndTools(t *testing.T) {
	for _, tc := range []struct {
		name         string
		check        func(string) error
		stream, want string
	}{
		{"antigravity", AntigravityResultError, "{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"partial\"}}\n{\"event\":\"result\",\"result\":{\"status\":\"ERROR\",\"response\":\"429 rate limit\"}}", "429 rate limit"},
		{"antigravity error field", AntigravityResultError, `{"event":"result","result":{"status":"ERROR","response":"","error":"Individual quota reached. Resets in 153h9m38s."}}`, "Individual quota reached"},
		{"antigravity mentions", AntigravityResultError, `{"event":"result","result":{"status":"SUCCESS","response":"We handle provider failures."}}`, ""},
		{"antigravity tool", AntigravityResultError, `{"event":"step_update","step_update":{"step_type":"tool","state":"ERROR"}}`, ""},
		{"opencode", OpenCodeError, `{"type":"error","error":{"name":"APIError","data":{"message":"credit exhausted"}}}`, "credit exhausted"},
		{"opencode mentions", OpenCodeError, `{"type":"text","part":{"text":"We handle credit exhaustion."}}`, ""},
		{"opencode tool", OpenCodeError, `{"type":"tool_use","part":{"state":{"status":"error","error":"tool failure"}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check(tc.stream)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
		})
	}
}
