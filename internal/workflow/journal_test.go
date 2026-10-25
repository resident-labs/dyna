package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestWorkerRestrictionUsesScratch(t *testing.T) {
	if !strings.Contains(workflowRestriction, "Put throwaway files (screenshots, cards, scripts, notes) in $DYNA_SCRATCH, never in a repository.") {
		t.Fatal("missing scratch instruction")
	}
}

func TestJournalWorkerPromptContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wf_prompt-contract", "agents", "7", "journal.jsonl")
	task := "Inspect the repository without changing it, then report findings."
	for _, required := range []string{
		"dyna journal",
		"read-only exploration tasks",
		"never restricts the file edits",
		"does not modify the target workspace",
		"one or two sentences plus an optional next step",
		"Keep working after every entry",
		"still return the final response",
		"separate from any final-response JSON schema",
		"running INSIDE a dyna workflow as a worker (workflow mode)",
		"you are a leaf",
		"Delegation is disabled",
		"dyna run and dyna subagent fail inside a workflow worker",
		"The only dyna commands you may use are dyna journal",
	} {
		if !strings.Contains(journalBoundary, required) {
			t.Errorf("journal instructions do not contain %q", required)
		}
	}
	prompt := workerTaskPrompt(task, path)
	if !strings.HasPrefix(prompt, "[ORIGINAL TASK]\n"+task+"\n\n[DYNA WORKER CONTEXT]\n") || !strings.HasSuffix(prompt, path+"\n[/DYNA WORKER CONTEXT]") {
		t.Fatalf("the task must come verbatim, then the journal path in trailing worker context:\n%s", prompt)
	}
}

func TestExecuteKeepsCallerPromptAndWritesCompleteAgentJournal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-journal-test")
	t.Setenv(runstore.AgentJournalRootEnv, "")

	const callerPrompt = `SLEEP:0
RESPOND: {"answer":"ok"}`
	const schemaJSON = `{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`
	script := fmt.Sprintf(`
const schema = JSON.parse(%s);
const result = await agent(%s, {
  profile: "mock-journal",
  label: "journaled worker",
  phase: "Verification",
  schema,
});
return result;
`, strconv.Quote(schemaJSON), strconv.Quote(callerPrompt))

	run, err := runstore.Create("engine journal test", script, nil)
	if err != nil {
		t.Fatalf("runstore.Create() error = %v", err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "mock-journal", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
	}}}
	// A hang guard only. The in-process mock emits its journal entry and
	// result without the default simulated work delay.
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Second)
	defer cancel()

	resultJSON, err := Execute(ctx, Options{
		ScriptSrc: script,
		Store:     store,
		Run:       run,
		WorkDir:   run.Dir,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resultJSON != `{"answer":"ok"}` {
		t.Fatalf("result = %s, want schema-valid mock result", resultJSON)
	}

	rootEntries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil {
		t.Fatalf("ReadJournal() error = %v", err)
	}
	if len(rootEntries) != 1 {
		t.Fatalf("root journal entries = %#v, want one completed call", rootEntries)
	}
	root := rootEntries[0]
	if root.Prompt != callerPrompt {
		t.Fatalf("root JournalEntry.Prompt = %q, want original caller prompt %q", root.Prompt, callerPrompt)
	}
	if strings.Contains(root.Prompt, "DYNA WORK JOURNAL") || strings.Contains(root.Prompt, "OUTPUT FORMAT") {
		t.Fatalf("root journal persisted an internal prompt wrapper: %q", root.Prompt)
	}
	wantKey := profileCallKey(store.Profiles[0], callerPrompt, schemaJSON, run.Dir, "journaled worker")
	if root.Cwd != run.Dir {
		t.Fatalf("journal cwd = %q, want %q", root.Cwd, run.Dir)
	}
	if root.Key != wantKey {
		t.Fatalf("call key = %q, want caller-prompt key %q", root.Key, wantKey)
	}
	wrappedKey := profileCallKey(store.Profiles[0], workerTaskPrompt(callerPrompt, "journal-path"), schemaJSON, run.Dir, "journaled worker")
	if root.Key == wrappedKey {
		t.Fatal("call key was derived from the journal instruction envelope")
	}

	agentEntries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatalf("ReadAgentJournalFrom() error = %v", err)
	}
	if len(agentEntries) != 3 {
		t.Fatalf("agent journal entries = %#v, want start + agent update + complete", agentEntries)
	}
	if start := agentEntries[0]; start.Kind != "start" || start.Source != "system" || start.AgentID != 1 || start.Prompt != callerPrompt {
		t.Fatalf("start entry = %#v", start)
	}
	if update := agentEntries[1]; update.Kind != "update" || update.Source != "agent" || update.Message == "" {
		t.Fatalf("agent-authored entry = %#v", update)
	}
	if complete := agentEntries[2]; complete.Kind != "complete" || complete.Source != "system" || complete.Message == "" {
		t.Fatalf("completion entry = %#v", complete)
	}
}

func TestWorkerPromptsOfferNoGuidesAndKeepSchemaLast(t *testing.T) {
	if strings.Contains(workflowRestriction, "dyna guide") || strings.Contains(journalBoundary, "dyna guide") {
		t.Fatal("workflow worker instructions must not offer guide access")
	}
	base := workerTaskPrompt("Complete the caller task.", "") + "\n\n---\nOUTPUT FORMAT: schema"
	if strings.Index(base, "Complete the caller task.") > strings.Index(base, "OUTPUT FORMAT") {
		t.Fatalf("schema wrapper did not remain last:\n%s", base)
	}
}

func TestWorkflowRestrictionPromptReachesEveryWorker(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "prompt.txt")
	worker := fakeworker.InstallClaude(t, `printf '%s\n%s' "$DYNA_FAKE_PROMPT" "$DYNA_FAKE_INSTRUCTIONS" > "$CAPTURE"; printf done`)
	worker.Name, worker.Default = "custom", true
	worker.Env = mergedEnv(worker.Env, map[string]string{"CAPTURE": capture})
	store := &profile.Store{Profiles: []profile.Profile{worker}}
	result, err := Execute(context.Background(), Options{
		ScriptSrc: `return await agent("caller task", {profile: "custom"});`,
		Store:     store, WorkDir: t.TempDir(),
	})
	if err != nil || result != `"done"` {
		t.Fatalf("Execute() = %s, %v", result, err)
	}
	prompt, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	// Workflow workers always run as leaves, so the restriction is always
	// present; there is no profile setting that turns it off.
	hasRestriction := strings.Contains(string(prompt), "DYNA WORKFLOW RESTRICTION")
	if !hasRestriction || !strings.Contains(string(prompt), "[ORIGINAL TASK]\ncaller task") {
		t.Fatalf("captured prompt = %q", prompt)
	}
}

func TestExecuteWaitsForDanglingWorkerTerminalJournal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-dangling-journal")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	// The fake claude reads its input only after a turn's body returns, and this
	// body blocks until the test ends, so a canceled turn waits the whole grace.
	// The wait is not what this test proves, so shorten it.
	grace := harness.SetClaudeInterruptGraceForTest(50 * time.Millisecond)
	t.Cleanup(func() { harness.SetClaudeInterruptGraceForTest(grace) })
	script := `
agent("keep working until the orchestrator finishes", {profile: "slow", label: "dangling worker"});
agent("wait behind the first worker", {profile: "slow", label: "queued worker"});
post("@root", "wait for a worker to start");
return "workflow done";
`
	run, err := runstore.Create("dangling journal test", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })

	// The worker signals actual process startup, then blocks on a pipe. The
	// workflow can finish only after that event; elapsed time does not order it.
	dir := t.TempDir()
	readyPath, holdPath := filepath.Join(dir, "ready"), filepath.Join(dir, "hold")
	for _, path := range []string{readyPath, holdPath} {
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	started := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, err := io.ReadFull(ready, signal[:])
		started <- err
	}()
	worker := fakeworker.InstallClaude(t, `printf ready > "$READY_FIFO"; read gate < "$HOLD_FIFO"`)
	worker.Name, worker.Default, worker.MaxConcurrent = "slow", true, 1
	worker.Env = mergedEnv(worker.Env, map[string]string{"READY_FIFO": readyPath, "HOLD_FIFO": holdPath})
	store := &profile.Store{Profiles: []profile.Profile{worker}}
	// A hang guard only. Both the ready signal and Execute's worker drain are
	// event-driven, including on a busy race-enabled CI runner.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resultJSON, err := Execute(ctx, Options{
		ScriptSrc: script, Store: store, Run: run, WorkDir: dir,
		Poster: func(_ context.Context, _ string, _ []string, _ string) (PostResult, error) {
			select {
			case err := <-started:
				return PostResult{ID: "worker-ready"}, err
			case <-ctx.Done():
				return PostResult{}, ctx.Err()
			}
		},
	})

	if err != nil || resultJSON != `"workflow done"` {
		t.Fatalf("Execute() = %s, %v", resultJSON, err)
	}

	events, err := runstore.ReadEvents(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundTerminal := map[int]bool{}
	running := 0
	for _, event := range events {
		if event.T == "agent_run" && (event.ID == 1 || event.ID == 2) {
			running++
		}
		if event.T == "agent_end" && (event.ID == 1 || event.ID == 2) && event.Status == "error" &&
			(strings.Contains(event.Error, "canceled/timed out") || strings.Contains(event.Error, "context canceled")) {
			foundTerminal[event.ID] = true
		}
	}
	if running != 1 {
		t.Fatalf("started %d workers; want one running and one queued", running)
	}
	if !foundTerminal[1] || !foundTerminal[2] {
		t.Fatalf("running/queued worker terminal events were not persisted before Execute returned: %#v", events)
	}

	entries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 || entries[0].Kind != "start" || entries[len(entries)-1].Kind != "error" || entries[len(entries)-1].Source != "system" {
		t.Fatalf("dangling worker journal = %#v, want terminal system error", entries)
	}
}

func TestJournalWorkersSharePrefixThroughIdenticalTask(t *testing.T) {
	const task = "Inspect the same repository and report findings.\nPreserve this exact task."
	first := workerTaskPrompt(task, "/runs/first/agents/1/journal.jsonl")
	second := workerTaskPrompt(task, "/runs/second/agents/9/journal.jsonl")
	a, b := strings.LastIndex(first, "\n\n[DYNA WORKER CONTEXT]\n"), strings.LastIndex(second, "\n\n[DYNA WORKER CONTEXT]\n")
	if a < 0 || b < 0 || first[:a] != second[:b] || !strings.HasSuffix(first[:a], task) || first == second {
		t.Fatalf("worker identity changed the shared task prefix:\n%s\n%s", first, second)
	}
}
