package home

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A Claude or Muse session names Dyna's shell MCP server in its config. That
// command must survive the turn that published it and stay the same on the
// next turn, or the second turn starts a server that cannot run.
func TestPrepareKeepsHarnessMCPCommandAcrossTurns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux running image copy")
	}
	if out := os.Getenv("DYNA_PREPARE_TURN_OUT"); out != "" {
		pinned := fmt.Sprintf("/proc/%d/exe", os.Getpid())
		prepared, err := Prepare(os.Getenv("DYNA_PREPARE_TURN_SCOPE"), pinned, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, []byte(prepared["DYNA_BIN"]), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	scope := filepath.Join(t.TempDir(), "scope")
	commands := make([]string, 0, 2)
	for turn := 0; turn < 2; turn++ {
		out := filepath.Join(t.TempDir(), fmt.Sprintf("turn-%d", turn))
		cmd := exec.Command(os.Args[0], "-test.run=TestPrepareKeepsHarnessMCPCommandAcrossTurns$")
		cmd.Env = append(os.Environ(), "DYNA_PREPARE_TURN_OUT="+out, "DYNA_PREPARE_TURN_SCOPE="+scope)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("turn %d failed: %v\n%s", turn, err, output)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		commands = append(commands, string(raw))
	}
	// Each turn ran in its own process, so its DYNA_BIN was a different
	// /proc/<pid>/exe. The published command must not follow that pid.
	if commands[0] != commands[1] {
		t.Fatalf("harness MCP command changed between turns: %q then %q", commands[0], commands[1])
	}
	target := strings.TrimSpace(strings.SplitN(readBridgeTarget(t, commands[0]), "\n", 1)[0])
	if strings.HasPrefix(target, "/proc/") {
		t.Fatalf("harness MCP command still names a process image: %s", target)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("harness MCP command is gone after its turns: %v", err)
	}
}

// readBridgeTarget returns the exec target of the published dyna bridge.
func readBridgeTarget(t *testing.T, dyna string) string {
	t.Helper()
	raw, err := os.ReadFile(dyna)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	start := strings.IndexByte(line, '\'')
	end := strings.LastIndexByte(line, '\'')
	if !strings.HasPrefix(line, "#!") && start < 0 {
		return dyna
	}
	if start < 0 || end <= start {
		return dyna
	}
	return line[start+1 : end]
}
