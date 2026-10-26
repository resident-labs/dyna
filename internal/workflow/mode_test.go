package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
)

// Workflow mode forces delegation off for every worker, injects the worker
// env contract, and never lets worker env leak into the profile store.
func TestWorkflowWorkersGetModeEnvAndForcedNoDelegation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("DYNA_SCRATCH_ROOT", filepath.Join(t.TempDir(), "scratch"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-mode-env")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	capture := filepath.Join(t.TempDir(), "env.txt")
	worker := fakeworker.InstallClaude(t, `printf '%s|%s|%s|%s|%s' "$DYNA_MODE" "$DYNA_WORKER_RUN" "$DYNA_WORKER_AGENT" "$DYNA_SCRATCH" "$(stat -c %a "$DYNA_SCRATCH")" > "$CAPTURE"; printf done`)
	worker.Name, worker.Default = "leafy", true
	worker.Env = mergedEnv(worker.Env, map[string]string{"CAPTURE": capture})
	store := &profile.Store{Profiles: []profile.Profile{worker}}
	run, err := runstore.Create("mode env", `return await agent("task")`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "", nil)
	if _, err := Execute(context.Background(), Options{
		ScriptSrc: `return await agent("task")`, Store: store, Run: run, WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(capture)
	if err != nil || string(got) != "workflow|wf_engine-mode-env|1|"+filepath.Join(scratch.Root(), run.Meta.ID, "agent-1")+"|700" {
		t.Fatalf("worker env = %q, %v", got, err)
	}
	if store.Profiles[0].Env["DYNA_MODE"] != "" {
		t.Fatal("worker env leaked into the shared profile store")
	}
}

func subagentOnlyTestStore(t *testing.T) *profile.Store {
	t.Helper()
	solo := fakeworker.InstallClaude(t, `printf solo`)
	// Marked default on purpose: it may be the configured default for subagent
	// sessions, but workflows must never pick it.
	solo.Name, solo.Default, solo.Uses = "solo", true, []string{profile.UseAgents}
	worker := fakeworker.InstallClaude(t, `printf worker`)
	worker.Name = "worker"
	return &profile.Store{Profiles: []profile.Profile{solo, worker}}
}

func TestWorkflowHidesLimitedProfilesFromProfilesGlobal(t *testing.T) {
	result, err := Execute(context.Background(), Options{
		ScriptSrc: `return profiles.map(p => p.name).join(",") + "|" + profiles.map(p => p.uses.join("+")).join(",") + "|" + profiles.map(p => String(p.disableSubagents)).join(",")`,
		Store:     subagentOnlyTestStore(t), WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != `"worker|workflows+agents+services|undefined"` {
		t.Fatalf("profiles global = %s, want only the workflow worker without the removed field", result)
	}
}

func TestWorkflowRejectsExplicitSubagentsOnlyProfile(t *testing.T) {
	result, err := Execute(context.Background(), Options{
		ScriptSrc: `try { return await agent("task", {profile: "solo"}); } catch (e) { return "rejected: " + String(e); }`,
		Store:     subagentOnlyTestStore(t), WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "rejected:") || !strings.Contains(result, "not enabled for workflows (uses: agents)") || strings.Contains(result, `"solo"`) && !strings.Contains(result, "profile") {
		t.Fatalf("explicit subagent-only agent() result = %s, want a clear subagent-only rejection", result)
	}
	if strings.Trim(result, `"`) == "solo" {
		t.Fatalf("subagent-only profile ran in a workflow: %s", result)
	}
}

func TestWorkflowDefaultSkipsSubagentsOnlyProfile(t *testing.T) {
	result, err := Execute(context.Background(), Options{
		ScriptSrc: `return await agent("task")`,
		Store:     subagentOnlyTestStore(t), WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != `"worker"` {
		t.Fatalf("default agent() ran %s, want the workflow-eligible worker (not the subagent-only default)", result)
	}
}

func TestWorkflowStartsWithScratchUnavailable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("DYNA_SCRATCH_ROOT", filepath.Join(tmp, "scratch"))
	t.Setenv("DYNA_SCRATCH", "/stale/parent")
	root := scratch.Root()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	worker := fakeworker.InstallClaude(t, `if [ "${DYNA_SCRATCH+x}" = x ]; then printf stale; else printf done; fi`)
	worker.Name, worker.Default = "p", true
	worker.Env = mergedEnv(worker.Env, map[string]string{"DYNA_SCRATCH": "/stale/profile"})
	store := &profile.Store{Profiles: []profile.Profile{worker}}
	result, err := Execute(context.Background(), Options{ScriptSrc: `return await agent("task")`, Store: store, WorkDir: t.TempDir()})
	if err != nil || result != `"done"` {
		t.Fatalf("worker did not run without scratch: %s %v", result, err)
	}
}
