package workflow

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
)

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
	result, err := executeTest(context.Background(), Options{
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
	result, err := executeTest(context.Background(), Options{
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
	result, err := executeTest(context.Background(), Options{
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
