package pr

import (
	"os"
	"testing"

	"dyna-agent/internal/mode"
)

// TestMain keeps the caller's worker identity out of temporary test stores.
func TestMain(m *testing.M) {
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
