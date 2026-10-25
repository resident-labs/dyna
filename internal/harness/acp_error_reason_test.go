package harness

import (
	"errors"
	"testing"

	"dyna-agent/internal/jsonrpc"
	"dyna-agent/internal/providerfailure"
)

// A JSON-RPC error reply is the provider's own answer, so its structured
// message and data name the reason. A transport or local failure is not an
// answer, so it stays untyped.
func TestACPPromptErrorReasons(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want providerfailure.Reason
	}{
		{"message names the sign-in", &jsonrpc.Error{Code: -32000, Message: "authentication_error: invalid api key"}, providerfailure.Auth},
		{"message names the quota", &jsonrpc.Error{Code: -32000, Message: "usage limit reached"}, providerfailure.Quota},
		{"data status names the sign-in", &jsonrpc.Error{Code: -32000, Message: "prompt failed", Data: []byte(`{"status":401}`)}, providerfailure.Auth},
		{"data status names the busy provider", &jsonrpc.Error{Code: -32000, Message: "prompt failed", Data: []byte(`{"status":503,"details":"try again"}`)}, providerfailure.Capacity},
		{"data type names the quota", &jsonrpc.Error{Code: -32000, Message: "prompt failed", Data: []byte(`{"type":"provider.quota"}`)}, providerfailure.Quota},
		{"opaque reply names nothing", &jsonrpc.Error{Code: -32000, Message: "prompt failed"}, ""},
		{"transport failure names nothing", &jsonrpc.TransportError{Cause: errors.New("closed")}, ""},
		{"local failure names nothing", errors.New("permission denied"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := providerfailure.ReasonOf(acpPromptError(tc.err))
			if ok != (tc.want != "") || reason != tc.want {
				t.Fatalf("reason = %q, %v; want %q", reason, ok, tc.want)
			}
		})
	}
}
