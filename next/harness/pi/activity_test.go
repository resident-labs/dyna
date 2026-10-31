package pi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActivityLabel(t *testing.T) {
	for _, tc := range []struct{ name, args, want string }{
		{"read", `{"path":"/a/spec.md"}`, "read spec.md"},
		{"bash", `{"command":"` + strings.Repeat("x", 90) + `\nignored"}`, "bash " + strings.Repeat("x", 90)},
		{"tool", `{"other":"value"}`, "tool"},
		{"tool", `["not an object"]`, "tool"},
	} {
		if got := activityLabel(event{ToolName: tc.name, Args: json.RawMessage(tc.args)}); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
