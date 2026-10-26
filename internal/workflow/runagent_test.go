package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow/enginefake"
)

type fakeEngine struct {
	calls  []string
	states []callspec.RunAgentState
	errs   []error
}

func (f *fakeEngine) Call(_ context.Context, method string, _ []any, result any) error {
	f.calls = append(f.calls, method)
	if method != "WaitRunAgent" {
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
