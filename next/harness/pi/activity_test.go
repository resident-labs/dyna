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
		{"bash", `{"command":"uptime","host":"hermeslab"}`, "bash on hermeslab: uptime"},
		{"job", `{"action":"output","job":"job_1"}`, "job job_1"},
		{"computer", `{"action":"click","x":10,"y":20,"status":"Opening the settings"}`, "computer Opening the settings"},
		{"computer", `{"action":"launch","command":"urxvt"}`, "computer urxvt"},
		{"tool", `{"other":"value"}`, "tool"},
		{"tool", `["not an object"]`, "tool"},
	} {
		if got := activityLabel(event{ToolName: tc.name, Args: json.RawMessage(tc.args)}); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
