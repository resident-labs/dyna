package home

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"dyna-agent/internal/fsutil"
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

// A tool home published for one engine build must keep calling a dyna an
// update keeps current. The wrapper names the dyna the owner installed, so
// the update that replaces that binary in place moves every running session
// on its next exec with no engine help; naming one build's copy instead left
// sessions on the build they first started with, and the updated state
// refused their calls as a version mismatch (D-161, D-171).
func TestPrepareBridgeFollowsTheCurrentBuild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux running image link")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "bin")
	write(t, filepath.Join(install, "dyna"), "#!/bin/sh\nprintf 'installed current'\n", 0700)
	write(t, filepath.Join(install, "git"), "#!/bin/sh\ntrue\n", 0700)
	env := []string{"PATH=" + install}
	scope := filepath.Join(t.TempDir(), "scope")
	before, err := Prepare(scope, buildStub(t, "build one"), env)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Prepare(scope, buildStub(t, "build two, updated"), env)
	if err != nil {
		t.Fatal(err)
	}
	if before["DYNA_BIN"] != after["DYNA_BIN"] {
		t.Fatalf("an engine update republished the Dyna command: %q then %q", before["DYNA_BIN"], after["DYNA_BIN"])
	}
	target := strings.TrimSpace(readBridgeTarget(t, after["DYNA_BIN"]))
	installed := filepath.Join(install, "dyna")
	if target != installed {
		t.Fatalf("published Dyna command names %q, want the installed dyna %q", target, installed)
	}
	out, err := exec.Command(after["DYNA_BIN"]).Output()
	if err != nil {
		t.Fatalf("published Dyna command does not run after an update: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "installed current" {
		t.Fatalf("published Dyna command runs %q, want the installed dyna", got)
	}
}

// buildStub copies a stand-in engine build into the image cache, as the engine
// does for the build it runs.
func buildStub(t *testing.T, build string) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(source, []byte("#!/bin/sh\nprintf '%s\\n' \""+build+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	image, err := fsutil.StableImage(source)
	if err != nil {
		t.Fatal(err)
	}
	return image
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
