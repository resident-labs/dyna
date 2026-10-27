package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

func TestMergeFailureReportFrozenEvidence(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3)
	f.branch, f.ci, f.conclusion = "dyna/merge-trial/1", "completed", "failure"
	f.jobs = []budgetJob{{Name: "Go tests", Status: "completed", Conclusion: "failure"}, {Name: "lint", Status: "completed", Conclusion: "success"}}
	b := state.MergeTrial{Branch: f.branch, Base: f.main, Head: f.head, DefaultBranch: "main", Logs: "Go tests\tRun tests\t2026-10-19T10:00:00Z --- FAIL: TestBroken/subcase (0.01s)\nGo tests\tRun tests\t2026-10-19T10:00:00Z FAIL\texample.org/repo/watch\t0.01s\nweb\tTest\t FAIL src/view.test.ts > shows result\n"}
	var member Record
	if err := f.e.s.Store.update(func(st *State) error {
		for _, n := range []int{3, 1, 2} {
			for _, r := range st.Records {
				if r.Number == n {
					b.Changes = append(b.Changes, state.MergeTrialChange{Key: r.Key, Head: fmt.Sprintf("%040x", n)})
					if n == 3 {
						member = *r
					}
				}
			}
		}
		r := st.Records[b.Changes[0].Key]
		r.Head = "new-live-head"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) != "git" {
			return run(ctx, cmd)
		}
		args := cmd.Args[1:]
		if strings.HasPrefix(args[0], "--work-tree=") {
			base := strings.TrimPrefix(args[0], "--work-tree=")
			if err := os.MkdirAll(filepath.Join(base, "watch"), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(base, "go.mod"), []byte("module example.org/repo\n"), 0600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(base, "watch/view_test.go"), []byte("package watch\nimport _ \"example.org/repo/shared\"\n"), 0600)
		}
		if args[0] == "diff" {
			compare := args[len(args)-2]
			file := "unrelated/thing.go"
			if strings.HasSuffix(compare, b.Changes[0].Head) {
				file = "watch/view.go"
			}
			if strings.HasSuffix(compare, b.Changes[1].Head) {
				file = "shared/helper.go"
			}
			fmt.Fprintf(cmd.Stdout, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", file, file, file, file)
		}
		return nil
	}
	report, err := f.e.s.mergeFailureReport(t.Context(), member, b, trialCI{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#3", "#1", "#2", "/pull/3", "/pull/1", "/pull/2", b.Changes[0].Head, "Go tests", "/actions/runs/81/attempts/1", "TestBroken/subcase", "example.org/repo/watch", "src/view.test.ts", "watch/view.go", "shared/helper.go", "not proof"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in report:\n%s", want, report)
		}
	}
	if strings.Index(report, "#3") > strings.Index(report, "#1") || strings.Index(report, "#1") > strings.Index(report, "#2") {
		t.Errorf("wrong frozen order:\n%s", report)
	}
	for _, unwanted := range []string{"unrelated/thing.go", "new-live-head"} {
		if strings.Contains(report, unwanted) {
			t.Errorf("unexpected %q:\n%s", unwanted, report)
		}
	}
	entries, err := os.ReadDir(filepath.Join(f.e.s.Store.Root, "trials"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("scratch files remain: %v", entries)
	}
}

func TestMergeFailureReportUnknownAndBounded(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	b := state.MergeTrial{Branch: "dyna/merge-trial/1", Changes: []state.MergeTrialChange{{Key: "missing", Head: "frozen"}}, Logs: strings.Repeat("unrelated noise\n", 10000)}
	report, err := s.mergeFailureReport(t.Context(), Record{}, b, trialCI{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "unknown") || !strings.Contains(report, "frozen") || strings.Contains(report, "unrelated noise") || len(report) > 16000 {
		t.Fatalf("bad unknown report: %s", report)
	}
}

func TestMergeFailureReportRejectsOtherRunEvidence(t *testing.T) {
	for _, scenario := range []string{"branch", "head", "event"} {
		t.Run(scenario, func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			f.branch, f.ci, f.conclusion = "dyna/merge-trial/1", "completed", "failure"
			f.jobs = []budgetJob{{Name: "unrelated job", Status: "completed", Conclusion: "failure"}}
			switch scenario {
			case "branch":
				f.observedBranch = "other"
			case "head":
				f.observedHead = strings.Repeat("a", 40)
			case "event":
				f.event = "pull_request"
			}
			var member Record
			st, err := f.e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range st.Records {
				member = *r
				break
			}
			report, err := f.e.s.mergeFailureReport(t.Context(), member, state.MergeTrial{Branch: f.branch, Head: f.head, Logs: "--- FAIL: TestBroken (0.01s)"}, trialCI{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(report, "unrelated job") || strings.Contains(report, "/actions/runs/81") || !strings.Contains(report, "unknown") {
				t.Fatalf("used unrelated evidence: %s", report)
			}
		})
	}
}

func TestMergeFailureReportBoundsFailureDetails(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	var logs strings.Builder
	for i := range 500 {
		fmt.Fprintf(&logs, "--- FAIL: Test%d%s (0.01s)\n", i, strings.Repeat("x", 1000))
	}
	report, err := s.mergeFailureReport(t.Context(), Record{}, state.MergeTrial{Logs: logs.String()}, trialCI{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report) > 16000 || strings.Contains(report, "Test499") || !strings.Contains(report, "Test0") {
		t.Fatalf("unbounded failure details: %d bytes", len(report))
	}
}

// A local CI suite failure is evidence even when no hosted log names a test.
// Trial 161's compile error reached the report only as "unknown": the failing
// go-static and go-test-shard suites held the file and the message in their
// saved local result.
func TestMergeFailureReportReadsLocalCISuiteFailures(t *testing.T) {
	ctx := t.Context()
	f := mergeTrialEnv(t, 1)
	f.branch, f.ci, f.conclusion = "dyna/merge-trial/1", "completed", "failure"
	repoDir := filepath.Join(t.TempDir(), "repo")
	if raw, err := exec.Command("git", "init", "--quiet", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	registry := localci.Store{DataDir: t.TempDir()}
	f.e.s.LocalCIRegistry = registry
	repo, err := localci.CanonicalRepo(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	failure := "FAIL dyna-agent/internal/room [build failed]\n" +
		"# dyna-agent/internal/room [dyna-agent/internal/room.test]\n" +
		"internal/room/events_test.go:521:7: duplicate case reflect.Bool (constant 1 of uint type reflect.Kind) in expression switch\n" +
		"\tinternal/room/events_test.go:502:14: previous case\n"
	if err := registry.SaveResult(ctx, localci.Result{Repo: repo, SHA: f.head, Node: "worker", Status: "failed", Checks: []localci.SuiteResult{
		{CheckName: "go-static", Status: "failed", Error: "exit status 1", LogTail: "# dyna-agent/internal/room\n" + failure},
		{CheckName: "go-test-shard (6)", Status: "failed", Error: "exit status 1", Failures: []string{failure}},
		// A check that never reached its tests names the cause instead of leaving
		// the report to say that it found no recognized failure.
		{CheckName: "go-test-shard (2)", Status: "error", Cause: localci.CauseSSHDrop, Error: "ssh: connect to host worker port 22: Connection timed out"},
	}}); err != nil {
		t.Fatal(err)
	}
	var member Record
	b := state.MergeTrial{Branch: f.branch, Base: f.main, Head: f.head, DefaultBranch: "main"}
	if err := f.e.s.Store.update(func(st *State) error {
		r := st.Records["github.com/owner/repo#1"]
		r.RepoPath = repoDir
		b.Changes = append(b.Changes, state.MergeTrialChange{Key: r.Key, Head: f.head})
		member = *r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) != "git" {
			return run(ctx, cmd)
		}
		args := cmd.Args[1:]
		if strings.HasPrefix(args[0], "--work-tree=") {
			base := strings.TrimPrefix(args[0], "--work-tree=")
			if err := os.MkdirAll(filepath.Join(base, "internal", "room"), 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(base, "go.mod"), []byte("module dyna-agent\n"), 0600)
		}
		if args[0] == "diff" {
			file := "internal/room/events_test.go"
			fmt.Fprintf(cmd.Stdout, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", file, file, file, file)
		}
		return nil
	}
	report, err := f.e.s.mergeFailureReport(ctx, member, b, trialCI{Status: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Local CI failures:", "go-static", "go-test-shard (6)", "go-test-shard (2) (SSH connection lost)", "duplicate case reflect.Bool", "events_test.go:521:7", "internal/room/events_test.go", "#1"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in report:\n%s", want, report)
		}
	}
	if strings.Contains(report, "no recognized test failure") || strings.Contains(report, "job evidence unavailable") {
		t.Errorf("report still calls the failure unknown:\n%s", report)
	}
}

// svelte-check prints a bare location and the message on the next line, with an
// absolute workspace path. The report must keep the message and point the
// culprit at the changed web/src file.
func TestMergeFailureReportKeepsSvelteCheckEvidence(t *testing.T) {
	ctx := t.Context()
	f := mergeTrialEnv(t, 1)
	f.branch, f.ci, f.conclusion = "dyna/merge-trial/1", "completed", "failure"
	repoDir := filepath.Join(t.TempDir(), "repo")
	if raw, err := exec.Command("git", "init", "--quiet", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	registry := localci.Store{DataDir: t.TempDir()}
	f.e.s.LocalCIRegistry = registry
	repo, err := localci.CanonicalRepo(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	output := "/workspace/web/src/App.svelte:12:3\nError: Type 'Issue[]' is not assignable to type 'RoomIssue[]'.\n"
	if err := registry.SaveResult(ctx, localci.Result{Repo: repo, SHA: f.head, Node: "worker", Status: "failed", Checks: []localci.SuiteResult{
		{CheckName: "frontend-check", Status: "failed", Error: "exit status 1", LogTail: output},
	}}); err != nil {
		t.Fatal(err)
	}
	var member Record
	b := state.MergeTrial{Branch: f.branch, Base: f.main, Head: f.head, DefaultBranch: "main"}
	if err := f.e.s.Store.update(func(st *State) error {
		r := st.Records["github.com/owner/repo#1"]
		r.RepoPath = repoDir
		b.Changes = append(b.Changes, state.MergeTrialChange{Key: r.Key, Head: f.head})
		member = *r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := f.e.s.GH.run
	f.e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) != "git" {
			return run(ctx, cmd)
		}
		args := cmd.Args[1:]
		if strings.HasPrefix(args[0], "--work-tree=") {
			base := strings.TrimPrefix(args[0], "--work-tree=")
			if err := os.MkdirAll(filepath.Join(base, "web", "src"), 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(base, "go.mod"), []byte("module dyna-agent\n"), 0600)
		}
		if args[0] == "diff" {
			file := "web/src/App.svelte"
			fmt.Fprintf(cmd.Stdout, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", file, file, file, file)
		}
		return nil
	}
	report, err := f.e.s.mergeFailureReport(ctx, member, b, trialCI{Status: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Local CI failures:", "frontend-check", "web/src/App.svelte:12:3", "Type 'Issue[]' is not assignable to type 'RoomIssue[]'", "#1"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in report:\n%s", want, report)
		}
	}
	if strings.Contains(report, "/workspace/") {
		t.Errorf("report kept the CI workspace prefix:\n%s", report)
	}
}
