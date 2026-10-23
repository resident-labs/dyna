package runtime

import (
	"context"
	"testing"
)

func testPrivateRuntime(t *testing.T) *Runtime {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := New()
	r.privateRecommendations = func(context.Context) []RecommendedVersion { return []RecommendedVersion{} }
	return r
}
