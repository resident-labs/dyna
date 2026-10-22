package enginetest

import (
	"context"
	"testing"

	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// ServePlanAncestry migrates the test's current data folder and serves ancestry writes.
func ServePlanAncestry(t testing.TB) ancestry.Store {
	t.Helper()
	s, calls := PlanAncestryCalls(t)
	Serve(t, s.DataDir, calls)
	return s
}

// PlanAncestryCalls is ServePlanAncestry's store and calls, for a test that
// serves them with other calls from one engine.
func PlanAncestryCalls(t testing.TB) (ancestry.Store, map[string]websvc.Call) {
	t.Helper()
	s := ancestry.Store{DataDir: store.DataDir()}
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(ctx context.Context, dir, parent string) (string, error) { return s.Stamp(ctx, dir, parent) },
	}})
	if err != nil {
		t.Fatal(err)
	}
	return s, calls
}
