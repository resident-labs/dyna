package localci

import (
	"context"
	"dyna-agent/internal/database"
	"strings"
	"testing"
	"time"
)

// A warm run is the provider's own cache-warming job, at the workflow path a
// repository check uses, and it reports nothing: it only fills the cache the
// checks later restore.
func TestWarmDefinitionRunsOnlyTheCacheJob(t *testing.T) {
	body, err := WarmDefinition(".github/workflows/ci.yml", "cache-warm")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := ParseDefinition(body)
	if err != nil {
		t.Fatalf("the warm definition does not parse: %v\n%s", err, body)
	}
	commands, ok := definition.ExecutableChecks(warmSuite)
	if !ok || len(commands) != 1 {
		t.Fatalf("warm suite = %v, want one check", commands)
	}
	check := commands[0]
	if commands[0].Job == nil || commands[0].Job.Name != "cache-warm" || commands[0].Job.Workflow != ".github/workflows/ci.yml" {
		t.Fatalf("warm check runs %+v, want the cache-warm job", commands[0].Job)
	}
	if check.Reports != ReportNone {
		t.Fatalf("warm check reports %q, want none", check.Reports)
	}
	if commands[0].Host {
		t.Fatal("a warm run must stay in act's containers")
	}
}

// A warm run without a job has nothing to run, so it is refused instead of
// starting a check that cannot fill the cache.
func TestWarmDefinitionNeedsAWorkflowAndJob(t *testing.T) {
	for _, tc := range []struct{ workflow, job string }{{"", "cache-warm"}, {".github/workflows/ci.yml", ""}} {
		if _, err := WarmDefinition(tc.workflow, tc.job); err == nil || !strings.Contains(err.Error(), "needs a workflow and a job") {
			t.Fatalf("WarmDefinition(%q, %q) = %v, want a refusal", tc.workflow, tc.job, err)
		}
	}
	if _, err := WarmDefinition("../ci.yml", "cache-warm"); err == nil {
		t.Fatal("a workflow outside .github/workflows was accepted")
	}
}

// Each machine remembers the commit it last warmed, so a main commit warms a
// machine once and the next commit warms it again.
func TestWarmStateKeepsTheHeadPerMachine(t *testing.T) {
	ctx := context.Background()
	store := Store{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(store.DataDir) })
	if head, err := store.WarmedHead(ctx, "repo", "hermeslab"); err != nil || head != "" {
		t.Fatalf("a machine that never warmed reports %q, %v", head, err)
	}
	first := strings.Repeat("a", 40)
	if err := store.SaveWarm(ctx, "repo", "hermeslab", first, time.Now()); err != nil {
		t.Fatal(err)
	}
	if head, _ := store.WarmedHead(ctx, "repo", "hermeslab"); head != first {
		t.Fatalf("hermeslab warmed %q, want %q", head, first)
	}
	// Another machine warms on its own, and a new commit replaces the head here.
	if head, _ := store.WarmedHead(ctx, "repo", "pve"); head != "" {
		t.Fatalf("pve took hermeslab's commit: %q", head)
	}
	second := strings.Repeat("b", 40)
	if err := store.SaveWarm(ctx, "repo", "hermeslab", second, time.Now()); err != nil {
		t.Fatal(err)
	}
	if head, _ := store.WarmedHead(ctx, "repo", "hermeslab"); head != second {
		t.Fatalf("hermeslab warmed %q, want the newer commit %q", head, second)
	}
}
