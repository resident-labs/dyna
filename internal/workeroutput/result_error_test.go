package workeroutput

import (
	"strings"
	"testing"

	"dyna-agent/internal/providerfailure"
)

// A Claude refusal result sets is_error but carries assistant text in result, so
// only the provider's own fields may name a reason.
func TestClaudeFailedResultReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		line   string
		want   providerfailure.Reason
		detail string
	}{
		{
			name:   "refusal prose names no provider",
			line:   `{"type":"result","subtype":"success","is_error":true,"result":"I cannot bypass the usage limit or the credit balance."}`,
			detail: "I cannot bypass the usage limit",
		},
		{
			name: "api error status names the quota",
			line: `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"opaque"}`,
			want: providerfailure.Quota,
		},
		{
			name: "api error status string names the sign-in",
			line: `{"type":"result","subtype":"success","is_error":true,"api_error_status":"401","result":"opaque"}`,
			want: providerfailure.Auth,
		},
		{
			name: "api error code names the sign-in",
			line: `{"type":"result","subtype":"success","is_error":true,"api_error_code":"authentication_error","result":"opaque"}`,
			want: providerfailure.Auth,
		},
		{
			name: "error list names the busy provider",
			line: `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["service unavailable"]}`,
			want: providerfailure.Capacity,
		},
		{
			name:   "structured output refusal names no provider",
			line:   `{"type":"result","subtype":"success","is_error":true,"api_error_status":0,"result":"quota exhausted"}`,
			detail: "quota exhausted",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ClaudeResultError(tc.line)
			if err == nil {
				t.Fatal("the failed result was reported as success")
			}
			if tc.detail != "" && !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("display text = %v, want %q", err, tc.detail)
			}
			reason, ok := providerfailure.ReasonOf(err)
			if ok != (tc.want != "") || reason != tc.want {
				t.Fatalf("reason = %q, %v; want %q", reason, ok, tc.want)
			}
		})
	}
}
