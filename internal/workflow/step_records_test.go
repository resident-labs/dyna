package workflow

import (
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
)

func TestCommandStepRecords(t *testing.T) {
	for _, tc := range []struct {
		name, trigger, wantError, state string
		starts                          int
	}{
		{name: "success", state: "result", starts: 1},
		{name: "intent write fails", trigger: `CREATE TRIGGER fail_step BEFORE INSERT ON workflow_step_records BEGIN SELECT RAISE(FAIL, 'intent blocked'); END`, wantError: "record command intent", starts: 0},
		{name: "result write fails", trigger: `CREATE TRIGGER fail_step BEFORE UPDATE ON workflow_step_records BEGIN SELECT RAISE(FAIL, 'result blocked'); END`, wantError: "record command result", state: "intent", starts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DYNA_DATA_DIR", t.TempDir())
			dir := store.DataDir()
			if err := runstore.Migrate(dir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close(dir) })
			run, err := runstore.Create("step records", "", nil)
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
			fake := newFakeCommands()
			fake.next = func(CommandSpec) (CommandExit, string) {
				var state string
				if err := db.QueryRow(`SELECT state FROM workflow_step_records WHERE run_id=?`, run.Meta.ID).Scan(&state); err != nil || state != "intent" {
					t.Errorf("before start: state %q, error %v", state, err)
				}
				return CommandExit{}, "hello"
			}
			_, _, err = runCommandScript(t, `return await run('echo hello')`, Options{Run: run, Commands: fake})
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if fake.attempts != tc.starts {
				t.Fatalf("starts = %d, want %d", fake.attempts, tc.starts)
			}
			if tc.state != "" {
				var state, externalID string
				var result *string
				if err := db.QueryRow(`SELECT state, external_id, result FROM workflow_step_records WHERE run_id=?`, run.Meta.ID).Scan(&state, &externalID, &result); err != nil {
					t.Fatal(err)
				}
				if state != tc.state {
					t.Fatalf("state = %q", state)
				}
				if state == "result" && (externalID != "sh_1" || result == nil || !strings.Contains(*result, "hello")) {
					t.Fatal(fmt.Sprintf("result = %v, external ID = %q", result, externalID))
				}
			}
		})
	}
}
