// Package enginefake stands in for the engine in tests of workflow runs. It
// answers the coordinator's run-agent calls by running each agent on its
// profile's harness in the test process, as the engine runs a leaf session.
package enginefake

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/structuredoutput"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workerprompt"
)

// Engine answers StartRunAgent, WaitRunAgent and CloseRunAgents. Like the
// real engine, it keeps an agent running when the call that started it ends:
// the agent stops at its deadline or when CloseRunAgents closes it.
type Engine struct {
	// Profiles are the registered profiles. Nil reads the profiles file.
	Profiles *profile.Store

	mu     sync.Mutex
	agents map[callspec.RunAgentRef]*agent
	starts []callspec.RunAgentStart
	closes int
}

type agent struct {
	cancel context.CancelFunc
	done   chan struct{}
	state  callspec.RunAgentState
}

// Starts lists the StartRunAgent requests received, in order.
func (f *Engine) Starts() []callspec.RunAgentStart {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]callspec.RunAgentStart(nil), f.starts...)
}

// Closes counts the CloseRunAgents calls received.
func (f *Engine) Closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// Call implements workflow.EngineCaller.
func (f *Engine) Call(ctx context.Context, method string, args []any, result any) error {
	switch method {
	case "StartRunAgent":
		start, _ := args[0].(callspec.RunAgentStart)
		return f.start(ctx, start)
	case "WaitRunAgent":
		ref, _ := args[0].(callspec.RunAgentRef)
		state, err := f.wait(ctx, ref)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, result)
	case "CloseRunAgents":
		run, _ := args[0].(string)
		return f.closeRun(ctx, run)
	}
	return fmt.Errorf("enginefake: unexpected call %s", method)
}

// Bindings serve the same calls on a test engine's socket.
func (f *Engine) Bindings() []websvc.Binding {
	return []websvc.Binding{
		{Name: "StartRunAgent", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: f.start},
		{Name: "WaitRunAgent", Access: websvc.Read, Scope: websvc.ScopeRead, Local: true, Method: f.wait},
		{Name: "CloseRunAgents", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: f.closeRun},
	}
}

func (f *Engine) closeRun(_ context.Context, run string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	for ref, a := range f.agents {
		if ref.Run == run {
			a.cancel()
		}
	}
	return nil
}

func (f *Engine) start(_ context.Context, start callspec.RunAgentStart) error {
	ref := callspec.RunAgentRef{Run: start.Run, Agent: start.Agent}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, start)
	if _, exists := f.agents[ref]; exists {
		return nil
	}
	profiles := f.Profiles
	if profiles == nil {
		var err error
		if profiles, err = profile.Load(profile.DefaultPath()); err != nil {
			return err
		}
	}
	prof, ok := profiles.Get(start.Profile)
	if !ok {
		return fmt.Errorf("unknown profile %q", start.Profile)
	}
	runCtx, cancel := context.WithDeadline(context.Background(), start.Deadline)
	a := &agent{cancel: cancel, done: make(chan struct{})}
	if f.agents == nil {
		f.agents = map[callspec.RunAgentRef]*agent{}
	}
	f.agents[ref] = a
	go func() {
		defer close(a.done)
		defer cancel()
		a.state = run(runCtx, *prof, start)
	}()
	return nil
}

func run(ctx context.Context, prof profile.Profile, start callspec.RunAgentStart) callspec.RunAgentState {
	prompt := start.Task
	var schema *structuredoutput.Schema
	if start.Schema != "" {
		var err error
		if schema, err = structuredoutput.Compile(start.Schema); err != nil {
			return callspec.RunAgentState{Done: true, Status: "error", Error: err.Error()}
		}
		prompt = workerprompt.AppendPromptSuffix(prompt, structuredoutput.SchemaSuffix(start.Schema))
	}
	// The session runner's environment carries the run's journal.
	env := maps.Clone(prof.Env)
	if env == nil {
		env = map[string]string{}
	}
	env[runstore.AgentJournalEnv], env[runstore.AgentJournalRootEnv] = start.Journal, start.JournalRoot
	prof.Env = env
	out, err := harness.RunWithOptions(ctx, prof, prompt, start.Dir, true,
		harness.JournalOptions{Path: start.Journal}, harness.SteeringOptions{}, harness.SessionOptions{},
		harness.ExecutionOptions{DisableNativeSubagents: true})
	if err != nil {
		return callspec.RunAgentState{Done: true, Status: "error", Error: err.Error()}
	}
	var value any = out.Output
	if schema != nil {
		if value, err = schema.Validate(out.Output); err != nil {
			return callspec.RunAgentState{Done: true, Status: "error", Error: fmt.Sprintf("schema validation failed: %v", err)}
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return callspec.RunAgentState{Done: true, Status: "error", Error: err.Error()}
	}
	return callspec.RunAgentState{Done: true, OK: true, Status: "ok", Result: raw}
}

func (f *Engine) wait(ctx context.Context, ref callspec.RunAgentRef) (callspec.RunAgentState, error) {
	f.mu.Lock()
	a := f.agents[ref]
	f.mu.Unlock()
	if a == nil {
		return callspec.RunAgentState{}, fmt.Errorf("no agent %d in run %s", ref.Agent, ref.Run)
	}
	select {
	case <-a.done:
		return a.state, nil
	case <-ctx.Done():
		return callspec.RunAgentState{}, ctx.Err()
	}
}
