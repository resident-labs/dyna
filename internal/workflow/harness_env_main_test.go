package workflow

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
	harnesshealth.VersionDetector = func(context.Context, profile.Profile) harnesshealth.VersionInfo {
		return harnesshealth.VersionInfo{Version: "test", Installed: true}
	}
	healthCache, cacheErr := os.MkdirTemp("", "dyna-health-tests-")
	if cacheErr != nil {
		panic(cacheErr)
	}
	os.Setenv("XDG_CACHE_HOME", healthCache)
	// The data folder is a temporary one unless a test sets its own, so no
	// test writes the user's own dyna.db.
	data, dataErr := os.MkdirTemp("", "dyna-workflow-data-")
	if dataErr != nil {
		panic(dataErr)
	}
	os.Setenv("XDG_DATA_HOME", data)

	// Tests set their own Dyna identity, journal paths, and runtime options.
	// Clear the full prefix so new parent-process settings cannot leak in.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			os.Unsetenv(key)
		}
	}
	os.Setenv("DYNA_COMPUTERS_ENABLED", "0")
	os.Unsetenv("CODEX_THREAD_ID")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("MUSE_SESSION_ID")
	// Fake harness CLIs know nothing about dyna's steering hook.
	os.Setenv("DYNA_STEER_HOOKS", "off")
	// Every engine test can launch workers. Keep their scratch files out of
	// the real /tmp/dyna, including tests without an individual setup helper.
	tmp, err := os.MkdirTemp("", "dyna-engine-tests-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("TMPDIR", tmp); err != nil {
		os.RemoveAll(tmp)
		panic(err)
	}
	// Scratch is a disk folder under the owner's cache now, not under TMPDIR,
	// so point it at this binary's temporary folder too.
	if err := os.Setenv("DYNA_SCRATCH_ROOT", tmp+"/scratch"); err != nil {
		os.RemoveAll(tmp)
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.RemoveAll(healthCache)
	os.RemoveAll(data)
	os.Exit(code)
}
