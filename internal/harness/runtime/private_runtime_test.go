package runtime

import (
	"context"
	"testing"

	"dyna-agent/internal/terminalsvc"
)

func testPrivateRuntime(t *testing.T) *Runtime {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := New()
	r.privateRecommendations = func(context.Context) []RecommendedVersion { return []RecommendedVersion{} }
	r.terminals = terminalsvc.New(terminalsvc.Options{})
	t.Cleanup(r.terminals.Shutdown)
	return r
}
