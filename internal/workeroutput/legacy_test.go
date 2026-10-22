package workeroutput

import (
	"strings"
	"testing"
)

func TestUpgradeLegacyMuseEvent(t *testing.T) {
	for _, tc := range []struct{ name, input, output, want string }{
		{"bash", "", `{"command":"ls","terminal_status":"exited","exit_code":1,"output":"failed"}`, `"command":"ls"`},
		{"tool", "{}", `{"command":"pwd","execution_state":"running","chunk_id":"1","output":"/tmp"}`, `"command":"pwd"`},
		{"read_file", "{}", "Read text file `a.go`.\ncontents", `"path":"a.go"`},
		{"write_file", "", "wrote 12 bytes to b.go; note: saved", `"path":"b.go"`},
	} {
		e := Event{Kind: "tool", Name: tc.name, Input: tc.input, Output: tc.output}
		got := UpgradeLegacyEvent(e)
		if !strings.Contains(got.Input, tc.want) {
			t.Fatalf("%s: %#v", tc.name, got)
		}
		again := UpgradeLegacyEvent(got)
		if again.Input != got.Input || again.Output != got.Output || again.Exit != got.Exit {
			t.Fatalf("upgrade is not idempotent: %#v", again)
		}
	}
}
