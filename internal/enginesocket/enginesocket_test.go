package enginesocket

import (
	"dyna-agent/internal/testsafe"
	"path/filepath"
	"testing"
)

// The socket follows the runtime folder, so every process on that runtime folder
// dials the same engine whatever data folder it names. The engine refuses a
// socket another data folder owns (internal/engine), which is what stops a
// second engine taking this one.
func TestPathFollowsTheRuntimeFolder(t *testing.T) {
	runtime := testsafe.ShortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	if got, err := Path(); err != nil || got != filepath.Join(runtime, "dyna", "engine.sock") {
		t.Fatalf("path = %q, %v", got, err)
	}
	for _, invalid := range []string{"", "relative"} {
		t.Setenv("XDG_RUNTIME_DIR", invalid)
		if _, err := Path(); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}
