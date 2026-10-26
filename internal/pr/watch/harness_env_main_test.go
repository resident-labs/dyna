package watch

import (
	"os"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/testsafe"
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
	// Test repositories are tiny and short-lived: skip background maintenance and fsync.
	testsafe.SetGitConfig("core.fsync", "none")
	code := m.Run()
	checkoutTemplates.Range(func(_, v any) bool {
		if dir := v.(*checkoutTemplate).dir; dir != "" {
			_ = os.RemoveAll(dir)
		}
		return true
	})
	os.Exit(code)
}
