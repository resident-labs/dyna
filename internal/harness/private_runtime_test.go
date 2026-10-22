package harness

import (
	"context"
	"path/filepath"
	"testing"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

// A preparation error returns the error and releases the turn lease.
func TestPrepareRuntimeErrorReleasesLeases(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	p := profile.Profile{Name: "override", Harness: profile.HarnessCodex, Env: map[string]string{"HOME": filepath.Join(root, "other")}}
	_, _, release, err := prepareRuntime(context.Background(), p, nil)
	if err == nil || release != nil {
		t.Fatalf("prepareRuntime() = release %v, %v; want an error and no release", release != nil, err)
	}
	if pending, running := harnessruntime.NewAccounts().SwitchStatus("codex"); pending || running != 0 {
		t.Fatal("the turn lease was not released", pending, running)
	}
}
