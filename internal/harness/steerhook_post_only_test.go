package harness

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSteerHookLeavesPreToolEventsAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunSteerHook(t.TempDir(), strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."},"cwd":"`+root+`"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("pre-tool response = %s; want no response", out.String())
	}
}
