package harness

import (
	"context"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/harness/fakeruntime"
	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
)

// TestMain replaces the private runtime with the fake one for every turn, so
// the fake CLIs on PATH run without an account or a managed bundle. It also
// lets the test binary act as a fake Pi for the RPC tests.
func TestMain(m *testing.M) {
	PrepareRuntime = fakeruntime.Prepare
	// No test account has a second sign-in, so a usage limit never moves one.
	FailoverAccount = func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		return nil, nil
	}
	harnesshealth.VersionDetector = func(context.Context, profile.Profile) harnesshealth.VersionInfo {
		return harnesshealth.VersionInfo{Version: "test", Installed: true}
	}
	healthCache, cacheErr := os.MkdirTemp("", "dyna-health-tests-")
	if cacheErr != nil {
		panic(cacheErr)
	}
	os.Setenv("XDG_CACHE_HOME", healthCache)

	// The fake pi runs before any DYNA_* reset: its mode variables arrive in
	// its own environment, set through the profile.
	if os.Getenv(envFakePi) != "" {
		fakePiMain()
		os.Exit(0)
	}
	// Normal tests set their own Dyna identity, journal paths and options.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			os.Unsetenv(key)
		}
	}
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	code := m.Run()
	os.RemoveAll(healthCache)
	os.Exit(code)
}
