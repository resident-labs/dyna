package workflows

import (
	"context"
	"dyna-agent/internal/testsafe"
	"io"
	"os"
	"testing"

	"dyna-agent/internal/runstore"
)

// The CLI changes runs only through the engine (requirement 1): with no
// engine, each write fails and the run is unchanged. The context is already
// canceled, so a call does not wait for the engine to start.
func TestRunWritesNeedTheEngine(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	run := finishedRun(t, "wf_no-engine")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{
		{"remove", run.Meta.ID},
		{"clear"},
		{"archive", run.Meta.ID},
		{"pause", run.Meta.ID},
	} {
		cmd := NewRunsCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(ctx); err == nil {
			t.Fatalf("%v without an engine succeeded", args)
		}
	}
	if info, err := os.Lstat(run.Dir); err != nil || !info.IsDir() || runstore.IsArchived(run.Meta.ID) {
		t.Fatalf("run after refused writes: %v, archived %v", err, runstore.IsArchived(run.Meta.ID))
	}
}

// Pause and resume go through the engine.
func TestPauseAndResumeThroughTheEngine(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveRunEngine(t)
	t.Setenv("DYNA_RUN_ID", "wf_pause")
	run, err := runstore.Create("pause", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := execRuns(t, "pause", run.Meta.ID); err != nil || !runstore.IsPaused(run.Meta.ID) {
		t.Fatalf("pause = %v, paused %v", err, runstore.IsPaused(run.Meta.ID))
	}
	if _, _, err := execRuns(t, "unpause", run.Meta.ID); err != nil || runstore.IsPaused(run.Meta.ID) {
		t.Fatalf("unpause = %v, paused %v", err, runstore.IsPaused(run.Meta.ID))
	}
	run.Finish("ok", "null", nil)
	if _, _, err := execRuns(t, "pause", run.Meta.ID); err == nil {
		t.Fatal("pausing a finished run succeeded")
	}
}
