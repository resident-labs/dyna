package goals_test

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/goals"
)

// The barrier for OpenHeldBy is named with the caller that asked for it, so a
// lifecycle check shows up in engine status instead of only goals.list.
func TestGoalBarrierNamesItsUpstreamCaller(t *testing.T) {
	database.SetCallerDetail(true)
	t.Cleanup(func() { database.SetCallerDetail(false) })
	s := &goals.Store{Root: filepath.Join(t.TempDir(), "goals")}
	if _, err := s.OpenHeldBy("r", "owner"); err != nil {
		t.Fatal(err)
	}
	const want = "dyna-agent/internal/goals.(*Store).list via dyna-agent/internal/goals_test.TestGoalBarrierNamesItsUpstreamCaller"
	for _, c := range database.Measurements().Callers {
		if c.Caller == want {
			return
		}
	}
	t.Errorf("no writer caller named %q in %v", want, database.Measurements().Callers)
}
