package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workflow/enginefake"
)

type recordingAgentEngine struct {
	*enginefake.Engine
	onStart  func(callspec.RunAgentStart)
	onWait   func()
	startErr error
	starts   int
}

func (e *recordingAgentEngine) Call(ctx context.Context, method string, args []any, result any) error {
	if method == "StartRunAgent" {
		e.starts++
		e.onStart(args[0].(callspec.RunAgentStart))
		if e.startErr != nil {
			return e.startErr
		}
	}
	if method == "WaitRunAgent" {
		e.onWait()
	}
	return e.Engine.Call(ctx, method, args, result)
}

func TestAgentStartStepRecords(t *testing.T) {
	for _, tc := range []struct {
		name, trigger, wantError, state string
		starts                          int
		refuse                          bool
	}{
		{name: "success", state: "result", starts: 1},
		{name: "start refused", state: "result", starts: 1, refuse: true, wantError: "start refused"},
		{name: "intent write fails", trigger: `CREATE TRIGGER fail_agent_step BEFORE INSERT ON workflow_step_records BEGIN SELECT RAISE(FAIL, 'intent blocked'); END`, wantError: "record agent start intent"},
		{name: "result write fails", trigger: `CREATE TRIGGER fail_agent_step BEFORE UPDATE ON workflow_step_records BEGIN SELECT RAISE(FAIL, 'result blocked'); END`, wantError: "record agent start result", state: "intent", starts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DYNA_DATA_DIR", t.TempDir())
			t.Setenv(runstore.AgentJournalRootEnv, "")
			dir := store.DataDir()
			if err := runstore.Migrate(dir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close(dir) })
			run, err := runstore.Create("agent records", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.trigger != "" {
				if _, err := db.Exec(tc.trigger); err != nil {
					t.Fatal(err)
				}
			}
			engine := &recordingAgentEngine{Engine: &enginefake.Engine{Profiles: mockWorkflowStore(0, 0)}}
			if tc.refuse {
				engine.startErr = errors.New("start refused")
			}
			engine.onWait = func() {
				var state, externalID string
				if err := db.QueryRow(`SELECT state, external_id FROM workflow_step_records WHERE run_id=?`, run.Meta.ID).Scan(&state, &externalID); err != nil || state != "result" || externalID != runstore.AgentSessionID(run.Meta.ID, 1) {
					t.Errorf("before wait: state %q, session ID %q, error %v", state, externalID, err)
				}
			}
			engine.onStart = func(start callspec.RunAgentStart) {
				var state string
				if err := db.QueryRow(`SELECT state FROM workflow_step_records WHERE run_id=?`, start.Run).Scan(&state); err != nil || state != "intent" {
					t.Errorf("before start: state %q, error %v", state, err)
				}
			}
			_, err = executeWorkflowTest(t, `return await agent("RESPOND: fine")`, Options{Store: mockWorkflowStore(0, 0), Run: run, Engine: engine, WorkDir: t.TempDir()})
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if got := engine.starts; got != tc.starts {
				t.Fatalf("starts = %d, want %d", got, tc.starts)
			}
			if tc.state != "" {
				var state, externalID string
				if err := db.QueryRow(`SELECT state, external_id FROM workflow_step_records WHERE run_id=?`, run.Meta.ID).Scan(&state, &externalID); err != nil {
					t.Fatal(err)
				}
				if state != tc.state {
					t.Fatalf("state = %q", state)
				}
				if state == "result" && !tc.refuse && externalID != runstore.AgentSessionID(run.Meta.ID, 1) {
					t.Fatalf("session ID = %q", externalID)
				}
			}
		})
	}
}
