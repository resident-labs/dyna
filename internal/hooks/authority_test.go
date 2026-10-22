package hooks

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

// The service is also the boundary for direct callers. Authorization must
// run before hook validation or Git resolves a caller-supplied repository.
func TestHookEditRulesPrecedeSideEffects(t *testing.T) {
	s := New()
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: "session", Room: "room"})
	for name, call := range map[string]func() error{
		"git hooks add":  func() error { return s.Add(ctx, "/missing/repository", Hook{}) },
		"git hooks edit": func() error { return s.Edit(ctx, "/missing/repository", "missing", "session", Patch{}) },
		"git hooks rm":   func() error { return s.Remove(ctx, "/missing/repository", "missing") },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), name+" is owner or root only") {
			t.Errorf("%s did not stop before side effects: %v", name, err)
		}
	}
}
