package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/callspec"
)

// EngineCaller is the engine connection a coordinator uses. *engineclient.Client
// satisfies it, and tests supply a fake.
type EngineCaller interface {
	Call(ctx context.Context, method string, args []any, result any) error
}

// runAgentSession has the engine start the leaf session that serves one
// agent() call, then waits for its first turn. Each wait call is bounded by the
// engine (callspec.RunAgentWaitBound), so a turn that runs for hours is a
// string of short calls, and a wait cut off by an engine restart is asked
// again. Starting twice is safe: the engine derives the session from the run
// and agent, and returns the session it already started.
func runAgentSession(ctx context.Context, engine EngineCaller, start callspec.RunAgentStart) (callspec.RunAgentState, error) {
	if err := engine.Call(ctx, "StartRunAgent", []any{start}, nil); err != nil {
		return callspec.RunAgentState{}, err
	}
	ref := callspec.RunAgentRef{Run: start.Run, Agent: start.Agent}
	for {
		var state callspec.RunAgentState
		err := engine.Call(ctx, "WaitRunAgent", []any{ref}, &state)
		switch {
		case err == nil && state.Done:
			return state, nil
		case err == nil, callspec.IsLate(err) && ctx.Err() == nil:
			// Still running, or the engine was too slow to answer: ask again.
		default:
			return callspec.RunAgentState{}, err
		}
	}
}

// agentResult reads the value an agent's first turn returned and its text for
// a preview. A turn that did not end with a result is the agent's error.
func agentResult(state callspec.RunAgentState) (any, string, error) {
	if !state.OK {
		if state.Error == "" {
			return nil, "", fmt.Errorf("agent turn ended %s", state.Status)
		}
		return nil, "", fmt.Errorf("%s", state.Error)
	}
	var value any
	if len(state.Result) > 0 {
		if err := json.Unmarshal(state.Result, &value); err != nil {
			return nil, "", fmt.Errorf("agent result is not valid JSON: %w", err)
		}
	}
	text, ok := value.(string)
	if !ok {
		text = string(state.Result)
	}
	return value, text, nil
}
