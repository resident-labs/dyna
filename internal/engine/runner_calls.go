package engine

import (
	"context"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// RunnerBindings are a detached turn runner's calls for its own session
// record (item 2). sessions is called on each call, so a table built before
// the engine's services start stays valid. Test engines that run real turn
// runners serve the same table.
func RunnerBindings(sessions func() *subagent.Manager) []websvc.Binding {
	return []websvc.Binding{
		{Name: "RunnerWaitInput", Access: websvc.Read, Scope: websvc.ScopeRead, Local: true, Method: func(ctx context.Context, input subagent.InputClaim) (bool, error) {
			return sessions().RunnerWaitInput(ctx, input)
		}},
		{Name: "RunnerInstructions", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, turn int) (subagent.FrozenInstructions, error) {
			return sessions().RunnerInstructions(ctx, turn)
		}},
		{Name: "RunnerClaimInput", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, input subagent.InputClaim) (subagent.ClaimedInput, error) {
			return sessions().RunnerClaimInput(ctx, input)
		}},
		{Name: "RunnerSetPid", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, pid int) error {
			return sessions().RunnerSetPid(ctx, pid)
		}},
		{Name: "RunnerSetRuntimePin", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, pin harnessruntime.Pin) error {
			return sessions().RunnerSetRuntimePin(ctx, pin)
		}},
		{Name: "RunnerSetInstructionsStale", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, stale bool) error {
			return sessions().RunnerSetInstructionsStale(ctx, stale)
		}},
		{Name: "RunnerSetHarnessSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, sid string) error {
			return sessions().RunnerSetHarnessSession(ctx, sid)
		}},
		{Name: "RunnerSetDMCursor", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, cursor int64) error {
			return sessions().RunnerSetDMCursor(ctx, cursor)
		}},
		{Name: "RunnerFinish", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, f subagent.RunnerFinish) error {
			return sessions().RunnerFinish(ctx, f)
		}},
		{Name: "RunnerTurnStart", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, start subagent.TurnStart) error {
			return sessions().RunnerTurnStart(ctx, start)
		}},
		{Name: "RunnerTurnEnd", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, end subagent.TurnEnd) error {
			return sessions().RunnerTurnEnd(ctx, end)
		}},
		{Name: "RunnerCompleteTurn", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, turn int) error {
			return sessions().RunnerCompleteTurn(ctx, turn)
		}},
		{Name: "RunnerReturnDormant", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, turn int) error {
			return sessions().RunnerReturnDormant(ctx, turn)
		}},
		{Name: "RunnerTurnNote", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, note subagent.TurnNote) error {
			return sessions().RunnerTurnNote(ctx, note)
		}},
	}
}

// runnerCalls are the names of the runner calls.
func runnerCalls() map[string]bool {
	names := map[string]bool{}
	for _, b := range RunnerBindings(nil) {
		if b.Access == websvc.Write {
			names[b.Name] = true
		}
	}
	return names
}
