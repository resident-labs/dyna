package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workflow/enginefake"
)

func TestWorkflowManifestCapturesCallResults(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	dataDir := store.DataDir()
	t.Cleanup(func() { database.Close(dataDir) })
	run, err := runstore.Create("manifest", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	commands := newFakeCommands()
	commands.next = func(CommandSpec) (CommandExit, string) { return CommandExit{}, "command output" }
	engine := &enginefake.Engine{Profiles: mockWorkflowStore(0, 0)}
	poster := func(context.Context, string, string, []string, string) (PostResult, error) {
		return PostResult{ID: "msg_1"}, nil
	}
	script := `await run('true'); await agent('RESPOND: fine'); post('@room', 'hello');`
	_, err = executeWorkflowTest(t, script, Options{Store: mockWorkflowStore(0, 0), Run: run, Engine: engine, Commands: commands, Poster: poster, WorkDir: t.TempDir(), RecordManifest: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.SealWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	manifest, err := runstore.ReadWorkflowManifest(t.Context(), run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[runstore.WorkflowCallKind]runstore.WorkflowManifestCall{}
	for _, call := range manifest.Calls {
		calls[call.Kind] = call
	}
	if len(calls) != 3 || !strings.Contains(string(calls[runstore.WorkflowCallCommand].Result), "command output") {
		t.Fatalf("calls = %+v", manifest.Calls)
	}
	var agent string
	if err := json.Unmarshal(calls[runstore.WorkflowCallAgent].Result, &agent); err != nil || agent != "fine" || calls[runstore.WorkflowCallAgent].ExternalID != runstore.AgentSessionID(run.Meta.ID, 1) {
		t.Fatalf("agent = %+v, %v", calls[runstore.WorkflowCallAgent], err)
	}
	var post PostResult
	if err := json.Unmarshal(calls[runstore.WorkflowCallPost].Result, &post); err != nil || post.ID != "msg_1" {
		t.Fatalf("post = %+v, %v", calls[runstore.WorkflowCallPost], err)
	}
}

func TestCaughtManifestWriteErrorStopsTheRun(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	dir := store.DataDir()
	t.Cleanup(func() { database.Close(dir) })
	run, err := runstore.Create("manifest write failure", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_manifest BEFORE INSERT ON workflow_manifest_calls BEGIN SELECT RAISE(FAIL, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	sent := false
	poster := func(context.Context, string, string, []string, string) (PostResult, error) {
		sent = true
		return PostResult{ID: "msg"}, nil
	}
	_, err = executeWorkflowTest(t, `try { post('@room', 'hello') } catch (_) {} return 'caught';`, Options{Store: postTestStore(), Run: run, Poster: poster, WorkDir: t.TempDir(), RecordManifest: true})
	if !errors.Is(err, ErrManifestWrite) || sent {
		t.Fatalf("error = %v, sent = %v", err, sent)
	}
}

func TestManifestIncludesAcceptedQueuedCalls(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	dir := store.DataDir()
	t.Cleanup(func() { database.Close(dir) })
	run, err := runstore.Create("queued manifest", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	engine := &enginefake.Engine{Profiles: mockWorkflowStore(0, 0)}
	script := `run('true'); agent('RESPOND: never started'); return 'done';`
	_, err = executeWorkflowTest(t, script, Options{Store: mockWorkflowStore(0, 0), Run: run, Engine: engine, Commands: newFakeCommands(), WorkDir: t.TempDir(), RecordManifest: true, Paused: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := runstore.ReadWorkflowManifest(t.Context(), run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Calls) != 2 {
		t.Fatalf("accepted calls = %+v; want command and agent", manifest.Calls)
	}
	if err := runstore.SealWorkflowManifest(t.Context(), run.Meta.ID); !errors.Is(err, runstore.ErrWorkflowManifestIncomplete) {
		t.Fatalf("seal queued calls = %v", err)
	}
}

type uncertainCommands struct{ *fakeCommands }

func (c uncertainCommands) Wait(context.Context, string) (CommandExit, error) {
	return CommandExit{}, errors.New("lost command status")
}

func TestManifestDoesNotCompleteUncertainCommand(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Cleanup(func() { database.Close(store.DataDir()) })
	run, err := runstore.Create("uncertain", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	_, err = executeWorkflowTest(t, `try { await run('true'); } catch (_) {} return 'done';`, Options{Store: mockWorkflowStore(0, 0), Run: run, Commands: uncertainCommands{newFakeCommands()}, WorkDir: t.TempDir(), RecordManifest: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.SealWorkflowManifest(t.Context(), run.Meta.ID); !errors.Is(err, runstore.ErrWorkflowManifestIncomplete) {
		t.Fatalf("seal uncertain command = %v", err)
	}
}
