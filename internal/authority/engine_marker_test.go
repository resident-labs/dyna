package authority

import (
	"os"
	"strconv"
	"testing"
)

func TestEngineMarkerIgnoresEnvironment(t *testing.T) {
	before := IsEngine()
	t.Setenv("DYNA_MODE", "owner")
	t.Setenv("DYNA_ENGINE_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("DYNA_HOOK_SESSION", "root")
	if IsEngine() != before {
		t.Fatal("environment changed the engine marker")
	}
	restore := InEngine()
	t.Cleanup(restore)
	if !IsEngine() {
		t.Fatal("engine startup did not establish its marker")
	}
	restore()
	if IsEngine() != before {
		t.Fatal("engine marker did not restore its prior state")
	}
}
