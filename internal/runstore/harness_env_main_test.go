package runstore

import (
	"os"
	"testing"

	"dyna-agent/internal/mode"
)

// TestMain keeps tests hermetic when they run inside an agent harness: the
// caller's Codex thread or Claude Code session would otherwise be recorded as
// the owner of every run and session the tests create.
func TestMain(m *testing.M) {
	os.Setenv("DYNA_COMPUTERS_ENABLED", "0")
	// Tests set their own worker identity and journal paths.
	for _, key := range mode.StampKeys() {
		os.Unsetenv(key)
	}
	for _, key := range []string{"DYNA_AGENT_JOURNAL", "DYNA_AGENT_JOURNAL_ROOT"} {
		os.Unsetenv(key)
	}
	os.Unsetenv("CODEX_THREAD_ID")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("MUSE_SESSION_ID")
	os.Exit(m.Run())
}
