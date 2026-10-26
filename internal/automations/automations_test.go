package automations

import (
	"testing"

	"dyna-agent/internal/hooks"
)

// The engine seeds only the table Hooks returns, so the doctor must be in it
// for `dyna git hooks edit doctor --global` to find a hook.
func TestDoctorHookIsSeededGlobalAndEnabled(t *testing.T) {
	s := hooks.New(Hooks(nil, nil)...)
	s.DataDir = t.TempDir()
	if err := s.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	global, err := s.ListGlobal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range global {
		if h.Name == "doctor" {
			if h.Builtin != BuiltinDoctor || h.Scope != hooks.ScopeGlobal || h.EveryMinutes != 1 || h.Disabled {
				t.Fatalf("doctor hook = %+v", h)
			}
			return
		}
	}
	t.Fatalf("no global doctor hook among %d seeded hooks", len(global))
}
