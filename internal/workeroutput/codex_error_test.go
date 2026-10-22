package workeroutput

import (
	"strings"
	"testing"
)

func TestCodexTurnErrorUsesTerminalProtocolNotAssistantText(t *testing.T) {
	for _, tc := range []struct{ name, stream, want string }{
		{"quota", `{"type":"turn.failed","error":{"message":"quota exhausted"}}`, "quota exhausted"},
		{"credit", `{"type":"error","message":"credit balance exhausted"}`, "credit balance exhausted"},
		{"usage", `{"type":"turn.failed","error":"usage limit reached"}`, "usage limit reached"},
		{"empty failure", `{"type":"turn.failed"}`, "turn.failed"},
		{"recovered", "{\"type\":\"error\",\"message\":\"429 retrying\"}\n{\"type\":\"turn.completed\"}", ""},
		{"later failure", "{\"type\":\"turn.completed\"}\n{\"type\":\"turn.failed\",\"error\":{\"message\":\"503 provider unavailable\"}}", "503 provider unavailable"},
		{"assistant mention", `{"type":"item.completed","item":{"type":"agent_message","text":"We tested a quota limit and 429 error."}}`, ""},
		{"tool failure", `{"type":"item.completed","item":{"type":"command_execution","status":"failed","exit_code":1}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CodexTurnError(tc.stream)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}
