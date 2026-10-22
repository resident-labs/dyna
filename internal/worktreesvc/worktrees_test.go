package worktreesvc

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/worktrees"
)

func TestMutationsRefuseMissingCallerBeforeOpeningStores(t *testing.T) {
	// The engine always supplies an actor. An empty actor must never fall
	// through to the owner environment, even if that environment is valid.
	ctx := authority.WithActor(t.Context(), authority.Actor{})
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"new", func(ctx context.Context) error {
			_, err := New(ctx, "missing", "feature", "origin/main", true, "")
			return err
		}},
		{"gc", func(ctx context.Context) error { _, err := GC(ctx, "missing", "", false); return err }},
		{"remove", func(ctx context.Context) error { _, err := Remove(ctx, []string{"missing"}, true); return err }},
		{"prune", func(ctx context.Context) error { _, err := Prune(ctx, 0, true, false); return err }},
		{"disposition", func(ctx context.Context) error {
			_, err := ApplyRunDisposition(ctx, "missing", worktrees.DispositionRemoveAll)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(ctx); err == nil || !strings.Contains(err.Error(), "could not verify") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCallerRefusesIncompleteIdentity(t *testing.T) {
	for _, actor := range []authority.Actor{{Role: authority.Root}, {Role: authority.Session}, {Role: authority.Worker}, {Role: "unknown"}} {
		if _, err := caller(authority.WithActor(t.Context(), actor)); err == nil {
			t.Fatalf("accepted incomplete actor %+v", actor)
		}
	}
}
