package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow/enginefake"
	"io"
)

type fakeEngine struct {
	startErrs []error
	calls     []string
	states    []callspec.RunAgentState
	errs      []error
}

func (f *fakeEngine) Call(_ context.Context, method string, _ []any, result any) error {
	f.calls = append(f.calls, method)
	if method != "WaitRunAgent" {
		if len(f.startErrs) > 0 {
			var err error
			err, f.startErrs = f.startErrs[0], f.startErrs[1:]
			return err
		}
		return nil
	}
	var err error
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	state := f.states[0]
	f.states = f.states[1:]
	b, _ := json.Marshal(state)
	_ = json.Unmarshal(b, result)
	return err
}

func TestRunAgentSessionStartsOnceAndAsksAgainUntilDone(t *testing.T) {
	engine := &fakeEngine{
		states: []callspec.RunAgentState{{}, {}, {Done: true, Status: "ok", Result: json.RawMessage(`"fine"`)}},
		errs:   []error{nil, callspec.Late("WaitRunAgent", time.Minute), nil},
	}
	state, err := runAgentSession(t.Context(), engine, callspec.RunAgentStart{Run: "wf_1", Agent: 2})
	if err != nil || string(state.Result) != `"fine"` {
		t.Fatalf("state = %+v, %v; want the result", state, err)
	}
	want := []string{"StartRunAgent", "WaitRunAgent", "WaitRunAgent", "WaitRunAgent"}
	if len(engine.calls) != len(want) {
		t.Fatalf("calls = %v; want %v", engine.calls, want)
	}
	for i := range want {
		if engine.calls[i] != want[i] {
			t.Fatalf("calls = %v; want %v", engine.calls, want)
		}
	}
}

func TestRunAgentSessionStopsOnAnEngineRefusal(t *testing.T) {
	refused := errors.New("refused")
	engine := &fakeEngine{states: []callspec.RunAgentState{{}}, errs: []error{refused}}
	if _, err := runAgentSession(t.Context(), engine, callspec.RunAgentStart{Run: "wf_1"}); !errors.Is(err, refused) {
		t.Fatalf("err = %v; want the refusal", err)
	}
}

func TestAgentCallAsksTheEngineForOneLeafSessionAndTheRunEndClosesIt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	script := `return await agent("RESPOND: fine", {label: "only", cwd: args.dir});`
	dir := t.TempDir()
	run, err := runstore.Create("run agent", script, map[string]string{"dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	engine := &enginefake.Engine{Profiles: mockWorkflowStore(0, 0)}
	got, err := executeWorkflowTest(t, script, Options{Store: mockWorkflowStore(0, 0), Run: run, Args: map[string]string{"dir": dir}, Engine: engine, WorkDir: dir})
	if err != nil || got != `"fine"` {
		t.Fatalf("result = %s, %v", got, err)
	}
	starts := engine.Starts()
	if len(starts) != 1 {
		t.Fatalf("StartRunAgent calls = %+v; want one", starts)
	}
	start := starts[0]
	if start.Inline == nil || start.Inline.Name != "mock" {
		t.Fatalf("a mock profile must come with the run, got %+v", start.Inline)
	}
	if start.Run != run.Meta.ID || start.Agent != 1 || start.Profile != "mock" || start.Dir != dir || start.Task != "RESPOND: fine" || start.Schema != "" {
		t.Fatalf("start = %+v", start)
	}
	wantRoot, _ := filepath.Abs(run.Dir)
	if start.JournalRoot != wantRoot || filepath.Dir(start.Journal) != filepath.Join(wantRoot, "agents", "1") || start.Deadline.IsZero() {
		t.Fatalf("journal = %q under %q, deadline %v", start.Journal, start.JournalRoot, start.Deadline)
	}
	if engine.Closes() != 1 {
		t.Fatalf("CloseRunAgents calls = %d; want one when the run ends", engine.Closes())
	}
}

func TestRunAgentSessionRetriesCallsAnEngineRestartCutsOff(t *testing.T) {
	engine := &fakeEngine{
		startErrs: []error{&engineclient.RemoteError{Status: 503, Message: "engine is stopping"}},
		states:    []callspec.RunAgentState{{}, {Done: true, OK: true, Result: json.RawMessage(`"fine"`)}},
		errs:      []error{io.EOF, nil},
	}
	state, err := runAgentSession(t.Context(), engine, callspec.RunAgentStart{Run: "wf_1", Agent: 1})
	if err != nil || !state.Done {
		t.Fatalf("state = %+v, %v; want the result after the restart", state, err)
	}
	if want := 4; len(engine.calls) != want {
		t.Fatalf("calls = %v; want a start retry and a wait retry (%d calls)", engine.calls, want)
	}
}

func TestRunAgentSessionKeepsARefusalTerminal(t *testing.T) {
	refusal := &engineclient.RemoteError{Status: 403, Message: "run agents need the run's verified coordinator"}
	engine := &fakeEngine{startErrs: []error{refusal}}
	if _, err := runAgentSession(t.Context(), engine, callspec.RunAgentStart{Run: "wf_1"}); !errors.Is(err, refusal) && err != error(refusal) {
		t.Fatalf("err = %v; want the refusal", err)
	}
	if len(engine.calls) != 1 {
		t.Fatalf("calls = %v; a refusal must not be retried", engine.calls)
	}
}

func TestNestedWorkflowThatEndsWithARunningAgentStopsItFirst(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	writeWorkflowScript(t, dir, "child.js", `agent("`+enginefake.HoldTask+`", {label: "left running"}); await agent("`+enginefake.AfterHeldTask+`"); return "child done";`)
	run, err := runstore.Create("nested", `return await workflow("child");`, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	engine := &enginefake.Engine{Profiles: mockWorkflowStore(0, 0)}
	got, err := executeWorkflowTest(t, `return await workflow("child");`, Options{Store: mockWorkflowStore(0, 0), Run: run, Engine: engine, WorkDir: dir, WorkflowDir: dir})
	if err != nil || got != `"child done"` {
		t.Fatalf("result = %s, %v", got, err)
	}
	if closed := engine.ClosedAgents(); len(closed) != 1 || closed[0].Agent != 1 {
		t.Fatalf("closed agents = %+v; want the nested script's agent closed when the child ended", closed)
	}
}
