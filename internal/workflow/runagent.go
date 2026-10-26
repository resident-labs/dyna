package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
)

// EngineCaller is the engine connection a coordinator uses. *engineclient.Client
// satisfies it, and tests supply a fake.
type EngineCaller interface {
	Call(ctx context.Context, method string, args []any, result any) error
}

// Retry pacing for engine calls that a restart cuts off. The first retry waits
// retryFirst and each next one twice as long, up to retryMax. A call that keeps
// failing gives up once recoveryBudget has passed since its first failure,
// counting the time the failed calls themselves took (the engine client waits
// up to a minute for a missing socket).
const (
	retryFirst     = 250 * time.Millisecond
	retryMax       = 5 * time.Second
	recoveryBudget = 5 * time.Minute
)

// retryWait waits d, or until ctx ends. Tests replace it.
var retryWait = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryNow is the clock of the recovery budget. Tests replace it.
var retryNow = time.Now

// retryEngine runs call, and runs it again after a pause while it fails for a
// reason an engine restart explains. With a budget it gives up once that much
// time has passed since the first failure; with none (0) it keeps asking until
// the call works or fails for another reason, for work that must be confirmed.
func retryEngine(ctx context.Context, budget time.Duration, call func() error) error {
	delay := retryFirst
	var firstFailure time.Time
	for {
		err := call()
		if err == nil || !transient(ctx, err) {
			return err
		}
		if firstFailure.IsZero() {
			firstFailure = retryNow()
		}
		if budget > 0 && retryNow().Sub(firstFailure) >= budget {
			return err
		}
		if retryWait(ctx, delay) != nil {
			return err
		}
		delay = min(2*delay, retryMax)
	}
}

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
	if err := retryEngine(ctx, recoveryBudget, func() error { return engine.Call(ctx, "StartRunAgent", []any{start}, nil) }); err != nil {
		return callspec.RunAgentState{}, err
	}
	ref := callspec.RunAgentRef{Run: start.Run, Agent: start.Agent}
	for {
		var state callspec.RunAgentState
		if err := retryEngine(ctx, recoveryBudget, func() error { return engine.Call(ctx, "WaitRunAgent", []any{ref}, &state) }); err != nil {
			return callspec.RunAgentState{}, err
		}
		if state.Done {
			return state, nil
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
