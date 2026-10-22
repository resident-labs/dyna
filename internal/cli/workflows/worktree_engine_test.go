package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/runsvc"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

// serveRunEngine serves the engine calls that change runs, as the owner on
// the engine's Unix socket, with any extra calls the test needs.
func serveRunEngine(t *testing.T) {
	t.Helper()
	serveRunEngineAt(t, storetest.DataDir(t))
}

// serveRunEngineAt keeps pre-bound calls and engine migrations on the same store.
func serveRunEngineAt(t *testing.T, dataDir string, extra ...map[string]websvc.Call) {
	t.Helper()
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "PauseRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: runsvc.Pause},
		{Name: "RemoveRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Remove},
		{Name: "ClearRuns", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Clear},
		{Name: "ArchiveRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Archive},
		{Name: "UnarchiveRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Unarchive},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			return call(authority.WithActor(ctx, authority.Actor{Role: authority.Owner}), args)
		}
	}
	for _, more := range extra {
		maps.Copy(calls, more)
	}
	enginetest.Serve(t, dataDir, calls)
}

func TestRunDeletionReportsEngineFailure(t *testing.T) {
	for _, command := range []string{"remove", "clear"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			run := finishedRun(t, "wf_engine-failure")
			refuse := func(context.Context, []json.RawMessage) (any, error) {
				return nil, errors.New("engine refused the removal")
			}
			enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{"RemoveRun": refuse, "ClearRuns": refuse})
			args := []string{command, "--worktrees=remove-all"}
			if command == "remove" {
				args = append(args, run.Meta.ID)
			}
			cmd := NewRunsCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "engine refused the removal") {
				t.Fatalf("%s = %v", command, err)
			}
			if _, err := os.Stat(run.Dir); err != nil {
				t.Fatalf("a refused removal removed the run: %v", err)
			}
		})
	}
}
