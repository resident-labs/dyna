package watch

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
	// Test repositories are tiny and short-lived: skip background maintenance and fsync.
	os.Setenv("GIT_CONFIG_COUNT", "3")
	os.Setenv("GIT_CONFIG_KEY_0", "maintenance.auto")
	os.Setenv("GIT_CONFIG_VALUE_0", "false")
	os.Setenv("GIT_CONFIG_KEY_1", "gc.auto")
	os.Setenv("GIT_CONFIG_VALUE_1", "0")
	os.Setenv("GIT_CONFIG_KEY_2", "core.fsync")
	os.Setenv("GIT_CONFIG_VALUE_2", "none")
	code := m.Run()
	checkoutTemplates.Range(func(_, v any) bool {
		if dir := v.(*checkoutTemplate).dir; dir != "" {
			_ = os.RemoveAll(dir)
		}
		return true
	})
	os.Exit(code)
}
