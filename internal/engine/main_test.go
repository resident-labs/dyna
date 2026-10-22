package engine

import (
	"io"
	"os"
	"testing"
)

// runnerEnv makes this test binary stand in for a live dyna process whose
// command line a test chose; it waits until its stdin closes.
const runnerEnv = "DYNA_ENGINE_TEST_RUNNER"

func TestMain(m *testing.M) {
	if os.Getenv(runnerEnv) == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
