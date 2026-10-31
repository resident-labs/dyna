package pi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActivityLabel(t *testing.T) {
	for _, tc := range []struct{ name, args, want string }{
		{"read", `{"path":"/a/spec.md"}`, "read spec.md"},
		{"bash", `{"command":"` + strings.Repeat("x", 90) + `\\nignored"}`, "bash " + strings.Repeat("x", 74) + "…"},
		{"tool", `{"other":"value"}`, "tool"},
	} {
		var args map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
			t.Fatal(err)
		}
		if got := activityLabel(event{ToolName: tc.name, Args: args}); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
