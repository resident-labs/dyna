package room

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	dynastore "dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// The token comes from stdin only, and reaches the engine as one call
// argument. Command output never echoes it, and a flag cannot carry it.
func TestMCPTokenSetReadsStdinAndNeverEchoes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	var got []string
	calls, err := websvc.Bind([]websvc.Binding{{Name: "RoomMCPSetToken", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(roomRef, name, url, token string) error {
		got = []string{name, url, token}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, dynastore.DataDir(), calls)
	cmd := mcpCmd()
	cmd.SetArgs([]string{"token", "set", "board", "--url", "https://mcp.example.test/mcp"})
	cmd.SetIn(strings.NewReader("fixture-token\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != "fixture-token" || strings.Contains(out.String(), "fixture-token") {
		t.Fatalf("call %v, output %q", got, out.String())
	}
	cmd = mcpCmd()
	cmd.SetArgs([]string{"token", "set", "board", "--url", "https://mcp.example.test/mcp", "--token", "x"})
	cmd.SetErr(&bytes.Buffer{})
	if cmd.Execute() == nil {
		t.Fatal("a token flag was accepted")
	}
}

func TestMCPAllowRefusesWriteAccess(t *testing.T) {
	cmd := mcpCmd()
	cmd.SetArgs([]string{"allow", "board", "claim_issue", "--write"})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "write tools are not available yet") {
		t.Fatalf("allow --write: %v", err)
	}
}
