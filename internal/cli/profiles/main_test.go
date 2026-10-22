package profiles

import (
	"context"
	"os"
	"testing"

	"dyna-agent/internal/harness/health"
	"dyna-agent/internal/mode"
)

// Tests never ask the npm registry whether a harness can be installed, and
// never write the user's own dyna.db: the data folder is a temporary one
// unless a test sets its own.
func TestMain(m *testing.M) {
	for _, key := range mode.StampKeys() {
		os.Unsetenv(key)
	}
	health.HarnessReady = func(context.Context, string, string) (bool, error) { return true, nil }
	data, err := os.MkdirTemp("", "dyna-profiles-data-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_DATA_HOME", data)
	code := m.Run()
	os.RemoveAll(data)
	os.Exit(code)
}
