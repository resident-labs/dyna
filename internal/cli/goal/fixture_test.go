package goal

import (
	"context"
	"encoding/json"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/goalkeeper"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// serveMutations runs the real shared service with a test-controlled caller.
func serveMutations(t *testing.T, service *goalkeeper.Service, actor *authority.Actor) {
	t.Helper()
	rooms := roomsvc.New(service.Notify)
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "AddGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.AddGoal},
		{Name: "EditGoalSelectors", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.EditGoalSelectors},
		{Name: "FinishGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.FinishGoal},
		{Name: "WaitGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.WaitGoal},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			return call(authority.WithActor(ctx, *actor), args)
		}
	}
	enginetest.Serve(t, store.DataDir(), calls)
}
