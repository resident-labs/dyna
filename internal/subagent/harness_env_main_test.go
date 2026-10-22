package subagent

import (
	"context"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/harness/fakeruntime"
	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
)

// TestMain keeps tests hermetic when they run inside an agent harness: the
// caller's Codex thread or Claude Code session would otherwise be recorded as
// the owner of every run and session the tests create. It also keeps the real
// Codex and Muse CLIs from being probed for steering-hook support.
func TestMain(m *testing.M) {
	harness.PrepareRuntime = fakeruntime.Prepare
	// A test that needs a turn held open says so with SLEEP:<ms>. Every other mock
	// turn would otherwise wait 400 ms, which was most of this package's run time.
	harness.MockDefaultDelay = 0
	harnesshealth.VersionDetector = func(context.Context, profile.Profile) harnesshealth.VersionInfo {
		return harnesshealth.VersionInfo{Version: "test", Installed: true}
	}
	healthCache, cacheErr := os.MkdirTemp("", "dyna-health-tests-")
	if cacheErr != nil {
		panic(cacheErr)
	}
	os.Setenv("XDG_CACHE_HOME", healthCache)

	// Tests set every Dyna option explicitly, including future options.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			os.Unsetenv(key)
		}
	}
	os.Unsetenv("CODEX_THREAD_ID")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("MUSE_SESSION_ID")
	os.Setenv("DYNA_COMPUTERS_ENABLED", "0")
	// Fake harness CLIs know nothing about dyna's steering hook.
	os.Setenv("DYNA_STEER_HOOKS", "off")
	code := m.Run()
	os.RemoveAll(healthCache)
	os.Exit(code)
}
