package workflows

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/cli/version"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
)

// TestMain keeps tests hermetic when they run inside an agent harness: the
// caller's Claude Code session would otherwise be recorded as
// the owner of every run and session the tests create.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "run-worker" || os.Args[1] == "workflow-test-cli") {
		callerid.WaitForLaunch()
		// mode.init clears inherited stamps in every test executable. These
		// gated subprocess fixtures model the production CLI, which retains them.
		initial, err := os.ReadFile("/proc/self/environ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, entry := range strings.Split(string(initial), "\x00") {
			key, value, _ := strings.Cut(entry, "=")
			for _, stampKey := range mode.StampKeys() {
				if key == stampKey {
					os.Setenv(key, value)
				}
			}
		}
		harness.MockDefaultDelay = 0
		if os.Args[1] == "run-worker" && os.Getenv(workflowTestControl) != "" {
			if err := slowWorkflowWorker(); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err)
				os.Exit(1)
			}
			os.Exit(0)
		}
		root := &cobra.Command{Use: "dyna", SilenceUsage: true}
		root.AddCommand(NewRunCommand(), NewDemoCommand(), NewRunWorkerCommand())
		args := os.Args[1:]
		if args[0] == "workflow-test-cli" {
			args = args[1:]
		}
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			os.Exit(version.CommandExitCode(err))
		}
		os.Exit(0)
	}
	os.Setenv("DYNA_COMPUTERS_ENABLED", "0")
	// Tests set their own worker identity and journal paths.
	for _, key := range mode.StampKeys() {
		os.Unsetenv(key)
	}
	for _, key := range []string{"DYNA_AGENT_JOURNAL", "DYNA_AGENT_JOURNAL_ROOT"} {
		os.Unsetenv(key)
	}
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("MUSE_SESSION_ID")
	os.Exit(m.Run())
}

// inRepo runs the test in a fresh git repo and data dir, so a run with no
// room uses that repo's room. t.Setenv also restores the stamp that
// applyRunRoom writes.
// mockProfile writes one default mock profile under a fresh XDG_CONFIG_HOME, so
// a run in this package needs no real harness.
func mockProfile(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dyna")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	store := &profile.Store{Path: filepath.Join(cfg, "profiles.json"), Profiles: []profile.Profile{{
		Name: "mock", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5,
	}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func inRepo(t *testing.T) {
	t.Helper()
	t.Setenv("DYNA_ROOM_ID", "")
	t.Setenv("DYNA_ROOM", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	t.Chdir(repo)
}
