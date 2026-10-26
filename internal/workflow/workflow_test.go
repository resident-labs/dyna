package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workflow/enginefake"
)

type workflowEventRecorder struct {
	mu     sync.Mutex
	events []runstore.Event
}

func (r *workflowEventRecorder) append(event runstore.Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *workflowEventRecorder) snapshot() []runstore.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runstore.Event(nil), r.events...)
}

func mockWorkflowStore(maxConcurrent, maxCalls int) *profile.Store {
	return &profile.Store{Profiles: []profile.Profile{{
		Name: "mock", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
		MaxConcurrent: maxConcurrent, MaxCallsPerRun: maxCalls,
	}}}
}

func writeWorkflowScript(t *testing.T, dir, name, src string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// executeTest runs a workflow with the fake engine unless the test brings its own.
func executeTest(ctx context.Context, opts Options) (string, error) {
	if opts.Engine == nil {
		opts.Engine = &enginefake.Engine{Profiles: opts.Store}
	}
	return Execute(ctx, opts)
}

func executeWorkflowTest(t *testing.T, script string, opts Options) (string, error) {
	t.Helper()
	if opts.Run != nil {
		dataDir := store.DataDir()
		if err := state.MigrateMirrors(dataDir); err != nil {
			t.Fatal(err)
		}
		if err := ancestry.Migrate(dataDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Close(dataDir) })
	}
	// A hang guard only. It stays above the 10 s per-test budget, so a slow
	// run is reported as slow instead of as a confusing deadline error.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opts.ScriptSrc = script
	return executeTest(ctx, opts)
}

func TestWorkflowResolvesSavedTemplateFromDefaultRegistry(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	registry := filepath.Join(cfg, "dyna", "workflows")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "echo.js", `return {received: args.value, source: "saved"};`)

	result, err := executeWorkflowTest(t, `return await workflow("echo", {value: 7});`, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var got struct {
		Received int    `json:"received"`
		Source   string `json:"source"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result = %q: %v", result, err)
	}
	if got.Received != 7 || got.Source != "saved" {
		t.Fatalf("result = %s, want saved-template child result", result)
	}
}

func TestWorkflowResolvesRegistryNameAndPassesArgs(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "echo.js", `return {received: args.value};`)

	result, err := executeWorkflowTest(t, `return await workflow("echo", {value: 42});`, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: dir, WorkflowDir: registry,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != `{"received":42}` {
		t.Fatalf("result = %s, want registry child result", result)
	}
}

func TestWorkflowNestingStopsAtOneLevel(t *testing.T) {
	dir := t.TempDir()
	rootPath := writeWorkflowScript(t, dir, "root.js", `return await workflow("./child.js");`)
	writeWorkflowScript(t, dir, "child.js", `return await workflow("./grandchild.js");`)
	writeWorkflowScript(t, dir, "grandchild.js", `return "must not execute";`)

	_, err := executeWorkflowTest(t, `return await workflow("./child.js");`, Options{
		ScriptPath: rootPath, Store: mockWorkflowStore(0, 0), WorkDir: dir,
	})
	if err == nil || !strings.Contains(err.Error(), "workflow nesting limit exceeded") || !strings.Contains(err.Error(), "maximum depth is 1") {
		t.Fatalf("Execute() error = %v, want clear one-level nesting diagnostic", err)
	}
}

func TestNestedWorkflowSharesLifetimeAgentCap(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `return await agent("nested", {profile: "mock"});`)
	recorder := &workflowEventRecorder{}
	script := `
return await parallel([
  () => workflow("child"),
  () => agent("sibling", {profile: "mock"}),
]);`
	resultJSON, err := executeWorkflowTest(t, script, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: dir, WorkflowDir: registry,
		MaxConc: 2, MaxAgents: 1, OnEvent: recorder.append,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result []any
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatal(err)
	}
	nulls := 0
	for _, value := range result {
		if value == nil {
			nulls++
		}
	}
	if len(result) != 2 || nulls != 1 {
		t.Fatalf("result = %s, want exactly one capped call", resultJSON)
	}
	starts := 0
	for _, event := range recorder.snapshot() {
		if event.T == "agent_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("agent_start count = %d, want one shared-cap launch", starts)
	}
}

func TestNestedWorkflowSharesConcurrencyLimits(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		maxConcurrent        int
		profileMaxConcurrent int
	}{
		{name: "run semaphore", maxConcurrent: 1},
		{name: "profile semaphore", maxConcurrent: 2, profileMaxConcurrent: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			dir := t.TempDir()
			registry := filepath.Join(dir, "registry")
			if err := os.Mkdir(registry, 0o755); err != nil {
				t.Fatal(err)
			}
			writeWorkflowScript(t, registry, "child.js", `return await agent("SLEEP:0\nRESPOND: nested", {profile: "mock"});`)
			recorder := &workflowEventRecorder{}
			// The gate waits for a competing worker's event, not elapsed time.
			// CI measures the 1 second budget; it must not cancel a scheduled worker.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			blocked, secondRun := make(chan struct{}), make(chan struct{})
			var blockedOnce, secondOnce sync.Once
			oldBlocked := slotBlocked
			slotBlocked = func() { blockedOnce.Do(func() { close(blocked) }) }
			t.Cleanup(func() { slotBlocked = oldBlocked })
			var startedMu sync.Mutex
			started := 0
			observe := func(event runstore.Event) {
				recorder.append(event)
				if event.T != "agent_run" {
					return
				}
				startedMu.Lock()
				started++
				n := started
				startedMu.Unlock()
				if n == 1 {
					select {
					case <-blocked:
					case <-secondRun:
					case <-ctx.Done():
					}
				} else if n == 2 {
					secondOnce.Do(func() { close(secondRun) })
				}
			}

			script := `return await parallel([
  () => workflow("child"),
  () => agent("SLEEP:0\nRESPOND: sibling", {profile: "mock"}),
]);`
			_, err := executeTest(ctx, Options{ScriptSrc: script,
				Store: mockWorkflowStore(tc.profileMaxConcurrent, 0), WorkDir: dir,
				WorkflowDir: registry, MaxConc: tc.maxConcurrent, OnEvent: observe,
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			running, peak := 0, 0
			for _, event := range recorder.snapshot() {
				switch event.T {
				case "agent_run":
					running++
					if running > peak {
						peak = running
					}
				case "agent_end":
					if event.Cached {
						continue
					}
					running--
				}
			}
			if peak != 1 || running != 0 {
				t.Fatalf("running peak/final = %d/%d, want 1/0; events=%#v", peak, running, recorder.snapshot())
			}
		})
	}
}

func TestNestedWorkflowSharesProfileCallLimit(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `return await agent("nested", {profile: "mock"});`)
	script := `return await parallel([
  () => workflow("child"),
  () => agent("sibling", {profile: "mock"}),
]);`
	_, err := executeWorkflowTest(t, script, Options{
		Store: mockWorkflowStore(0, 1), WorkDir: dir, WorkflowDir: registry, MaxConc: 2,
	})
	if err == nil || !strings.Contains(err.Error(), `profile "mock" call limit exceeded (1 per run)`) {
		t.Fatalf("Execute() error = %v, want shared profile call cap abort", err)
	}
}

func TestNestedWorkflowPersistsChildArtifactsAndUsesParentResumeCache(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `
phase("Child phase");
return await agent('SLEEP:0\nRESPOND: {"answer":"nested"}', {profile: "mock", schema: {type: "object"}});`)
	rootScript := `phase("Parent phase"); return await workflow("child", {value: 1});`

	t.Setenv("DYNA_RUN_ID", "wf_nested-first")
	first, err := runstore.Create("nested first", rootScript, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkflowTest(t, rootScript, Options{
		Store: mockWorkflowStore(0, 0), Run: first, WorkDir: dir, WorkflowDir: registry,
	})
	first.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(first.Meta.ID)
	if err != nil || len(entries) != 1 || entries[0].Workflow != "nested-1" {
		t.Fatalf("parent journal = %#v, %v", entries, err)
	}
	childDir := filepath.Join(first.Dir, "workflows", "nested-1")
	for _, name := range []string{"script.js", "events.jsonl", "journal.jsonl", "result.json"} {
		if _, err := os.Stat(filepath.Join(childDir, name)); err != nil {
			t.Fatalf("missing child artifact %s: %v", name, err)
		}
	}
	// No reader ever used the child's meta.json; the parent's events carry it.
	if _, err := os.Stat(filepath.Join(childDir, "meta.json")); !os.IsNotExist(err) {
		t.Fatalf("child meta.json: %v; want none", err)
	}
	childJournal, err := os.ReadFile(filepath.Join(childDir, "journal.jsonl"))
	if err != nil || !strings.Contains(string(childJournal), `"workflow":"nested-1"`) {
		t.Fatalf("child journal = %q, %v", childJournal, err)
	}
	events, err := runstore.ReadEvents(first.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundStart, foundNestedAgent, foundEnd := false, false, false
	for _, event := range events {
		if event.T == "workflow_start" && event.Workflow == "nested-1" && event.Parent == first.Meta.ID && event.Phase == "Parent phase" {
			foundStart = true
		}
		if event.T == "agent_start" && event.Workflow == "nested-1" && event.Phase == "Child phase" {
			foundNestedAgent = true
		}
		if event.T == "workflow_end" && event.Workflow == "nested-1" && event.Status == "ok" {
			foundEnd = true
		}
	}
	if !foundStart || !foundNestedAgent || !foundEnd {
		t.Fatalf("nested relationship missing from parent events: %#v", events)
	}

	t.Setenv("DYNA_RUN_ID", "wf_nested-resumed")
	second, err := runstore.Create("nested resumed", rootScript, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = executeWorkflowTest(t, rootScript, Options{
		Store: mockWorkflowStore(0, 0), Run: second, WorkDir: dir, WorkflowDir: registry,
		Cache: NewCache(entries),
	})
	second.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	resumedEvents, err := runstore.ReadEvents(second.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	cached := false
	for _, event := range resumedEvents {
		if event.T == "agent_end" && event.Workflow == "nested-1" && event.Cached {
			cached = true
		}
	}
	if !cached {
		t.Fatalf("nested agent did not use parent resume cache: %#v", resumedEvents)
	}
	resumedEntries, err := runstore.ReadJournal(second.Meta.ID)
	if err != nil || len(resumedEntries) != 1 || !resumedEntries[0].Cached || resumedEntries[0].Workflow != "nested-1" {
		t.Fatalf("resumed parent journal = %#v, %v", resumedEntries, err)
	}
	if _, err := os.Stat(filepath.Join(second.Dir, "workflows", "nested-1", "journal.jsonl")); err != nil {
		t.Fatalf("resumed child journal is not discoverable: %v", err)
	}
}

func TestProfilesGlobalOmitsAdmissionCost(t *testing.T) {
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "worker", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true,
	}}}
	result, err := executeTest(context.Background(), Options{
		ScriptSrc: `return {keys: Object.keys(profiles[0]).sort(), admissionCost: profiles[0].admissionCost};`,
		Store:     store, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Keys          []string `json:"keys"`
		AdmissionCost any      `json:"admissionCost"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result = %s: %v", result, err)
	}
	if got.AdmissionCost != nil {
		t.Fatalf("profiles[0].admissionCost = %#v, want undefined", got.AdmissionCost)
	}
	for _, key := range got.Keys {
		if key == "admissionCost" {
			t.Fatalf("profiles global keys = %v, want admissionCost omitted", got.Keys)
		}
	}
}
