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
	"dyna-agent/internal/jobs"
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
	// CloseErrs are returned, one per CloseRunAgent call, before it succeeds.
	CloseErrs []error

	mu          sync.Mutex
	agents      map[callspec.RunAgentRef]*agent
	starts      []callspec.RunAgentStart
	closes      int
	agentCloses []callspec.RunAgentRef
	held        chan struct{}
	heldOnce    sync.Once
	heldStarted sync.Once

	// JobHold keeps submitted jobs unfinished until its channel closes. Nil
	// finishes each job when WaitJob arrives. SaveJob seeds a resumed job.
	JobHold <-chan struct{}
	// JobSubmitErrors returns errors after storing new jobs, simulating a lost
	// submission response. A retry with the same key recovers the stored row.
	JobSubmitErrors []error
	jobMu           sync.Mutex
	jobRows         map[string]jobs.Job
	jobStarts       []callspec.RunJobInput
	jobWaits        []string
	jobStops        []string
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
	case "CloseRunAgent":
		ref, _ := args[0].(callspec.RunAgentRef)
		return f.closeAgent(ctx, ref)
	case "CloseRunAgents":
		run, _ := args[0].(string)
		return f.closeRun(ctx, run)
	case "RunJob":
		spec, _ := args[0].(callspec.RunJobInput)
		return f.runJob(spec, result)
	case "WaitJob":
		id, _ := args[0].(string)
		return f.waitJob(ctx, id, result)
	case "CancelJob":
		id, _ := args[0].(string)
		return f.cancelJob(id, result)
	}
	return fmt.Errorf("enginefake: unexpected call %s", method)
}

// SaveJob seeds a job that a resumed workflow can wait for.
func (f *Engine) SaveJob(job jobs.Job) {
	f.jobMu.Lock()
	defer f.jobMu.Unlock()
	if f.jobRows == nil {
		f.jobRows = map[string]jobs.Job{}
	}
	f.jobRows[job.ID] = job
}

// JobStarts lists the typed RunJob requests received, in order.
func (f *Engine) JobStarts() []callspec.RunJobInput {
	f.jobMu.Lock()
	defer f.jobMu.Unlock()
	return append([]callspec.RunJobInput(nil), f.jobStarts...)
}

// JobCount counts distinct stored jobs, excluding idempotent retries.
func (f *Engine) JobCount() int {
	f.jobMu.Lock()
	defer f.jobMu.Unlock()
	return len(f.jobRows)
}

// JobWaits lists the job IDs passed to WaitJob, in order.
func (f *Engine) JobWaits() []string {
	f.jobMu.Lock()
	defer f.jobMu.Unlock()
	return append([]string(nil), f.jobWaits...)
}

// JobStops lists the job IDs passed to CancelJob, in order.
func (f *Engine) JobStops() []string {
	f.jobMu.Lock()
	defer f.jobMu.Unlock()
	return append([]string(nil), f.jobStops...)
}

func (f *Engine) runJob(spec callspec.RunJobInput, result any) error {
	f.jobMu.Lock()
	defer f.jobMu.Unlock()
	f.jobStarts = append(f.jobStarts, spec)
	if f.jobRows == nil {
		f.jobRows = map[string]jobs.Job{}
	}
	if spec.IdempotencyKey != "" {
		for _, existing := range f.jobRows {
			if existing.IdempotencyKey == spec.IdempotencyKey {
				if existing.RequestHash != spec.RequestHash {
					return fmt.Errorf("enginefake: job key %s reused with a different request", spec.IdempotencyKey)
				}
				return writeJobResult(result, existing)
			}
		}
	}
	job := jobs.Job{
		ID: fmt.Sprintf("job_%d", len(f.jobRows)+1), Node: spec.Node,
		IdempotencyKey: spec.IdempotencyKey, RequestHash: spec.RequestHash, Command: spec.Command, State: jobs.StateQueued,
	}
	if job.Node == "" {
		job.Node = "local"
	}
	f.jobRows[job.ID] = job
	if len(f.JobSubmitErrors) > 0 {
		err := f.JobSubmitErrors[0]
		f.JobSubmitErrors = f.JobSubmitErrors[1:]
		return err
	}
	return writeJobResult(result, job)
}

func (f *Engine) waitJob(ctx context.Context, id string, result any) error {
	f.jobMu.Lock()
	f.jobWaits = append(f.jobWaits, id)
	job, ok := f.jobRows[id]
	f.jobMu.Unlock()
	if !ok {
		return fmt.Errorf("enginefake: no job %s", id)
	}
	if !job.State.Finished() {
		if f.JobHold != nil {
			select {
			case <-f.JobHold:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		f.jobMu.Lock()
		job = f.jobRows[id]
		if !job.State.Finished() {
			code := 0
			job.State, job.ExitCode = jobs.StatePassed, &code
			f.jobRows[id] = job
		}
		f.jobMu.Unlock()
	}
	return writeJobResult(result, jobs.WaitResult{Job: job, Done: true})
}

func (f *Engine) cancelJob(id string, result any) error {
	f.jobMu.Lock()
	defer f.jobMu.Unlock()
	f.jobStops = append(f.jobStops, id)
	job, ok := f.jobRows[id]
	if !ok {
		return fmt.Errorf("enginefake: no job %s", id)
	}
	if !job.State.Finished() {
		job.State, job.ExitCode = jobs.StateCancelled, nil
		f.jobRows[id] = job
	}
	return writeJobResult(result, job)
}

func writeJobResult(result any, value any) error {
	if result == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}

// Bindings serve the same calls on a test engine's socket.
func (f *Engine) Bindings() []websvc.Binding {
	return []websvc.Binding{
		{Name: "StartRunAgent", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: f.start},
		{Name: "WaitRunAgent", Access: websvc.Read, Scope: websvc.ScopeRead, Local: true, Method: f.wait},
		{Name: "CloseRunAgent", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: f.closeAgent},
		{Name: "CloseRunAgents", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: f.closeRun},
	}
}

func (f *Engine) closeAgent(_ context.Context, ref callspec.RunAgentRef) error {
	f.mu.Lock()
	f.agentCloses = append(f.agentCloses, ref)
	if len(f.CloseErrs) > 0 {
		err := f.CloseErrs[0]
		f.CloseErrs = f.CloseErrs[1:]
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	// Join the harness run. Cancel alone returns while the turn's health
	// write is still creating files under the test data directory.
	f.stopAgents(func(got callspec.RunAgentRef) bool { return got == ref })
	return nil
}

// Stop cancels every agent and waits until its harness run has finished,
// including the health write after the turn. A test registers Stop with
// Cleanup after TempDir so that write cannot recreate files during removal.
func (f *Engine) Stop() { f.stopAgents(nil) }

// stopAgents cancels the selected agents and waits for their runs. The caller
// must not hold f.mu: the run can finish only after this wait releases it.
func (f *Engine) stopAgents(match func(callspec.RunAgentRef) bool) {
	f.mu.Lock()
	agents := make([]*agent, 0, len(f.agents))
	for ref, a := range f.agents {
		if match == nil || match(ref) {
			agents = append(agents, a)
		}
	}
	f.mu.Unlock()
	for _, a := range agents {
		a.cancel()
	}
	for _, a := range agents {
		<-a.done
	}
}

// ClosedAgents lists the CloseRunAgent calls received, in order.
func (f *Engine) ClosedAgents() []callspec.RunAgentRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]callspec.RunAgentRef(nil), f.agentCloses...)
}

// HoldTask is a task that makes the fake's agent wait until it is closed.
const HoldTask = "HOLD UNTIL CLOSED"

func (f *Engine) closeRun(_ context.Context, run string) error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
	f.stopAgents(func(ref callspec.RunAgentRef) bool { return ref.Run == run })
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
	if start.Inline != nil {
		// Like the engine, take only a mock profile the run brings itself.
		if start.Inline.Harness != profile.HarnessMock {
			return fmt.Errorf("profile %q: only a mock profile can come with the run", start.Profile)
		}
		f.launch(start, ref, *start.Inline)
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
	f.launch(start, ref, *prof)
	return nil
}

// launch runs the agent on prof in the background. The caller holds f.mu.
func (f *Engine) launch(start callspec.RunAgentStart, ref callspec.RunAgentRef, prof profile.Profile) {
	runCtx, cancel := context.WithDeadline(context.Background(), start.Deadline)
	a := &agent{cancel: cancel, done: make(chan struct{})}
	if f.agents == nil {
		f.agents = map[callspec.RunAgentRef]*agent{}
	}
	f.agents[ref] = a
	go func() {
		defer close(a.done)
		defer cancel()
		a.state = f.run(runCtx, prof, start)
	}()
}

// AfterHeldTask is a task that makes the fake's agent finish once an agent with
// HoldTask has started, so a test can order two agents without a timer.
const AfterHeldTask = "FINISH AFTER A HELD AGENT STARTED"

func (f *Engine) heldChannel() chan struct{} {
	f.heldOnce.Do(func() { f.held = make(chan struct{}) })
	return f.held
}

func (f *Engine) run(ctx context.Context, prof profile.Profile, start callspec.RunAgentStart) callspec.RunAgentState {
	if start.Task == AfterHeldTask {
		select {
		case <-f.heldChannel():
			return callspec.RunAgentState{Done: true, OK: true, Status: "ok", Result: []byte(`"ok"`)}
		case <-ctx.Done():
			return callspec.RunAgentState{Done: true, Status: "stopped", Error: ctx.Err().Error()}
		}
	}
	if start.Task == HoldTask {
		f.heldStarted.Do(func() { close(f.heldChannel()) })
		<-ctx.Done()
		return callspec.RunAgentState{Done: true, Status: "stopped", Error: ctx.Err().Error()}
	}
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
