package localci

import (
	"context"
	"dyna-agent/internal/database"
	"path/filepath"
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

// A repository names the job that fills its act cache, so the warm run and the
// checks that restore the cache come from the same definition file.
func TestDefinitionNamesTheWarmJob(t *testing.T) {
	body := "version: 1\ntrial: [go]\nwarm: { workflow: .github/workflows/ci.yml, name: cache-warm }\nchecks:\n  - id: build\n    image: golang:1.26.3\n    command: go build ./...\nsuites:\n  - id: go\n    checks: [build]\n"
	definition, err := ParseDefinition([]byte(body))
	if err != nil {
		t.Fatalf("a definition with a warm job did not parse: %v", err)
	}
	warm, ok := definition.Warm()
	if !ok || warm.Workflow != ".github/workflows/ci.yml" || warm.Name != "cache-warm" {
		t.Fatalf("warm = %+v, %v", warm, ok)
	}
	// A definition without one simply has no cache to warm.
	plain, err := ParseDefinition([]byte("version: 1\ntrial: [go]\nchecks:\n  - id: build\n    image: golang:1.26.3\n    command: go build ./...\nsuites:\n  - id: go\n    checks: [build]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.Warm(); ok {
		t.Fatal("a definition without a warm job reports one")
	}
	for _, bad := range []string{"warm: { workflow: ci.yml, name: cache-warm }", "warm: { workflow: .github/workflows/ci.yml, name: 'cache warm' }"} {
		if _, err := ParseDefinition([]byte("version: 1\ntrial: [go]\n" + bad + "\nchecks:\n  - id: build\n    image: golang:1.26.3\n    command: go build ./...\nsuites:\n  - id: go\n    checks: [build]\n")); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// A warm run sends the default branch's push event, because the workflow scopes
// its cache-warming job to that ref, and its payload names the branch.
func TestWarmRunSendsTheDefaultBranchPush(t *testing.T) {
	dir := t.TempDir()
	check := nodeCheck{id: "cache-warm", name: "cache-warm", event: "push", branch: "main", job: &JobRef{Workflow: ".github/workflows/ci.yml", Name: "cache-warm"}, resources: checkResources{CPUs: 2, MemoryMiB: 4096}}
	script := nodeCheckScript(Node{Name: "local"}, dir, filepath.Join(dir, "cache.git"), dir, "run-warm", strings.Repeat("a", 40), strings.Repeat("b", 40), check, time.Minute, 0, false, DefaultActImage, "", "")
	if !strings.Contains(script, "act push -W") && !strings.Contains(script, " push -W ") {
		t.Fatalf("the warm run does not send a push event: %s", script)
	}
	if !strings.Contains(script, `refs/heads/main`) {
		t.Fatalf("the warm payload does not name the default branch: %s", script)
	}
}

// A warm run works in the checkout the caller named, because the runner reads
// Repo as a local directory, and it does not save a result: a cache-only run
// must not replace the test evidence real checks saved for the same commit.
func TestWarmRunUsesTheCheckoutAndSavesNoResult(t *testing.T) {
	definition, err := ParseDefinition([]byte("version: 1\ntrial: [cache-warm]\nchecks:\n  - id: cache-warm\n    job: { workflow: .github/workflows/ci.yml, name: cache-warm }\nsuites:\n  - id: cache-warm\n    checks: [cache-warm]\n"))
	if err != nil {
		t.Fatal(err)
	}
	request := warmRunRequest(definition, WarmRequest{
		Repo: "github.com/owner/repo", RepoDir: "/checkouts/owner/repo", RoomID: "room_dyna",
		SHA: strings.Repeat("a", 40), Workflow: ".github/workflows/ci.yml", Job: "cache-warm", Branch: "main", Timeout: time.Minute,
	})
	if request.Repo != "/checkouts/owner/repo" || request.RepoDir != "/checkouts/owner/repo" {
		t.Fatalf("the warm run works in %q and %q, want the checkout", request.Repo, request.RepoDir)
	}
	if !request.DeferSave {
		t.Fatal("a warm run would save its result over the commit's test evidence")
	}
	if request.Event != "push" || request.Branch != "main" {
		t.Fatalf("warm run event = %q on %q, want the default branch's push", request.Event, request.Branch)
	}
}
