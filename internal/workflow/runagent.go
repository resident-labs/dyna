package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"syscall"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
)

// EngineCaller is the engine connection a coordinator uses. *engineclient.Client
// satisfies it, and tests supply a fake.
type EngineCaller interface {
	Call(ctx context.Context, method string, args []any, result any) error
}

// maxTransientCalls is how many engine calls in a row may fail for a reason
// that a restart explains before the agent call fails. Each failed call
// already waited for the engine to come back (engineclient), so this bounds
// minutes, not a spin.
const maxTransientCalls = 20

// transient reports an engine call failure that an engine restart explains: no
// engine on the socket, a 5xx while it shuts down, a call it did not answer in
// time, or a connection cut off. A refusal (4xx) or a canceled caller is not.
func transient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var remote *engineclient.RemoteError
	if errors.As(err, &remote) {
		return !remote.Final()
	}
	return errors.Is(err, engineclient.ErrNotRunning) || callspec.IsLate(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

// runAgentSession has the engine start the leaf session that serves one
// agent() call, then waits for its first turn. Each wait call is bounded by the
// engine (callspec.RunAgentWaitBound), so a turn that runs for hours is a
// string of short calls. A call that an engine restart cuts off is asked again
// while the caller's context lives: starting twice is safe (the engine derives
// the session from the run and agent and returns the one it started), and so
// is waiting twice.
func runAgentSession(ctx context.Context, engine EngineCaller, start callspec.RunAgentStart) (callspec.RunAgentState, error) {
	failures := 0
	retry := func(call func() error) error {
		for {
			err := call()
			if err == nil || !transient(ctx, err) {
				return err
			}
			if failures++; failures >= maxTransientCalls {
				return err
			}
		}
	}
	if err := retry(func() error { return engine.Call(ctx, "StartRunAgent", []any{start}, nil) }); err != nil {
		return callspec.RunAgentState{}, err
	}
	failures = 0
	ref := callspec.RunAgentRef{Run: start.Run, Agent: start.Agent}
	for {
		var state callspec.RunAgentState
		if err := retry(func() error { return engine.Call(ctx, "WaitRunAgent", []any{ref}, &state) }); err != nil {
			return callspec.RunAgentState{}, err
		}
		if state.Done {
			return state, nil
		}
		failures = 0
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
