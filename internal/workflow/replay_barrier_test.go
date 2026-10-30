package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workflow/enginefake"
	"testing"

	"dyna-agent/internal/runstore"
)

func TestReplayBarrierMatchesBeforeAllowingNewEffects(t *testing.T) {
	barrier, err := NewReplayBarrier(runstore.WorkflowManifest{Complete: true, Calls: []runstore.WorkflowManifestCall{
		{Kind: runstore.WorkflowCallCommand, Identity: "command", Ordinal: 1, Result: json.RawMessage(`{"code":0}`)},
		{Kind: runstore.WorkflowCallPost, Identity: "post", Ordinal: 0, Result: json.RawMessage(`{"id":"msg"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, replay, err := barrier.Match(runstore.WorkflowCallCommand, "command", 1); err != nil || !replay {
		t.Fatalf("command match = %v, %v", replay, err)
	}
	if _, replay, err := barrier.Match(runstore.WorkflowCallPost, "post", 0); err != nil || !replay {
		t.Fatalf("post match = %v, %v", replay, err)
	}
	if _, replay, err := barrier.Match(runstore.WorkflowCallAgent, "new", 1); err != nil || replay {
		t.Fatalf("new call = %v, %v", replay, err)
	}
	if err := barrier.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestReplayBarrierRejectsMissingAndChangedCalls(t *testing.T) {
	manifest := runstore.WorkflowManifest{Complete: true, Calls: []runstore.WorkflowManifestCall{{Kind: runstore.WorkflowCallCommand, Identity: "old", Ordinal: 1, Result: json.RawMessage(`null`)}}}
	barrier, err := NewReplayBarrier(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := barrier.Match(runstore.WorkflowCallCommand, "changed", 1); err == nil {
		t.Fatal("allowed a new effect before the old call matched")
	}
	if err := barrier.Finish(); err == nil {
		t.Fatal("accepted a missing call")
	}
	manifest.Complete = false
	if _, err := NewReplayBarrier(manifest); err == nil {
		t.Fatal("accepted an incomplete manifest")
	}
}

func TestWorkflowReplaysCallsWithoutEffects(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Cleanup(func() { database.Close(store.DataDir()) })
	script := `const x = await run('true'); const a = await agent('RESPOND: fine'); const p = post('@room', 'hello'); return [x.stdout, a, p.id];`
	workDir := t.TempDir()
	commands := newFakeCommands()
	commands.next = func(CommandSpec) (CommandExit, string) { return CommandExit{}, "output" }
	engine := &enginefake.Engine{Profiles: mockWorkflowStore(0, 0)}
	posts := 0
	poster := func(context.Context, string, string, []string, string) (PostResult, error) {
		posts++
		return PostResult{ID: "msg_1"}, nil
	}
	first, err := runstore.Create("first", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), first.Meta.ID); err != nil {
		t.Fatal(err)
	}
	opts := Options{Store: mockWorkflowStore(0, 0), Run: first, WorkDir: workDir, Engine: engine, Commands: commands, Poster: poster, RecordManifest: true, JobNamespace: first.Meta.ID}
	want, err := executeWorkflowTest(t, script, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.SealWorkflowManifest(t.Context(), first.Meta.ID); err != nil {
		t.Fatal(err)
	}
	manifest, err := runstore.ReadWorkflowManifest(t.Context(), first.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := NewReplayBarrier(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runstore.Create("second", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), second.Meta.ID); err != nil {
		t.Fatal(err)
	}
	opts.Run, opts.Replay = second, barrier
	got, err := executeWorkflowTest(t, script, opts)
	if err != nil || got != want {
		t.Fatalf("replay = %s, %v; want %s", got, err, want)
	}
	if commands.attempts != 1 || len(engine.Starts()) != 1 || posts != 1 {
		t.Fatalf("effects: %d commands, %d agents, %d posts", commands.attempts, len(engine.Starts()), posts)
	}
}

func TestWorkflowReplayRejectsChangedCommandTimeout(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Cleanup(func() { database.Close(store.DataDir()) })
	dir := t.TempDir()
	run, err := runstore.Create("timeout", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	commands := newFakeCommands()
	opts := Options{Store: mockWorkflowStore(0, 0), Run: run, WorkDir: dir, Commands: commands, RecordManifest: true}
	if _, err := executeWorkflowTest(t, `return await run('true', {timeout: 1});`, opts); err != nil {
		t.Fatal(err)
	}
	if err := runstore.SealWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	manifest, err := runstore.ReadWorkflowManifest(t.Context(), run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	opts.Replay, err = NewReplayBarrier(manifest)
	if err != nil {
		t.Fatal(err)
	}
	opts.RecordManifest = false
	_, err = executeWorkflowTest(t, `return await run('true', {timeout: 2});`, opts)
	if !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("changed timeout = %v", err)
	}
	if commands.attempts != 1 {
		t.Fatalf("started %d commands", commands.attempts)
	}
}

func TestReplayBarrierChecksSynchronousPostOrderAtNextAsyncCall(t *testing.T) {
	manifest := runstore.WorkflowManifest{Complete: true, Calls: []runstore.WorkflowManifestCall{
		{Kind: runstore.WorkflowCallCommand, Identity: "first", Ordinal: 1, Result: json.RawMessage(`null`)},
		{Kind: runstore.WorkflowCallPost, Identity: "second", Ordinal: 0, Result: json.RawMessage(`{}`)},
	}}
	barrier, err := NewReplayBarrier(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, replay, err := barrier.Match(runstore.WorkflowCallPost, "second", 0); err != nil || !replay {
		t.Fatalf("post = %v, %v", replay, err)
	}
	if _, _, err := barrier.Match(runstore.WorkflowCallCommand, "first", 1); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("out-of-order calls = %v", err)
	}
}
