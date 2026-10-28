package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

// testGit runs one git command in dir and returns its trimmed output.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := gitCommand(ctx, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// testCommit writes one file and commits it, so a test controls the history.
func testCommit(t *testing.T, dir, name, body, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", "-A")
	testGit(t, dir, "commit", "-q", "--no-gpg-sign", "-m", message)
}

// A rebase moves the base under a PR. The review input must diff against the
// current base and show only CI evidence for the assigned head, not the base
// commits or the CI result that the earlier head left behind.
func TestReviewInputUsesCurrentBaseAndAssignedHeadChecks(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	repo := t.TempDir()
	testGit(t, repo, "init", "-q", "-b", "main")
	testGit(t, repo, "config", "user.name", "test")
	testGit(t, repo, "config", "user.email", "test@example.com")
	testCommit(t, repo, "base.txt", "base\n", "base")
	oldBase := testGit(t, repo, "rev-parse", "HEAD")
	testGit(t, repo, "checkout", "-q", "-b", "feature")
	testCommit(t, repo, "pr.txt", "the pull request\n", "the PR commit")
	testGit(t, repo, "checkout", "-q", "main")
	testCommit(t, repo, "merged.txt", "a base commit merged after the fork\n", "merged on main")
	newBase := testGit(t, repo, "rev-parse", "HEAD")
	testGit(t, repo, "checkout", "-q", "feature")
	testGit(t, repo, "rebase", "-q", "main")
	head := testGit(t, repo, "rev-parse", "HEAD")

	r.Kind, r.Host, r.Repo, r.Number = "github", "github.com", "owner/repo", 17
	r.RepoPath, r.Base, r.Head = repo, oldBase, head
	r.Round = 2
	// The stored checks belong to the head of the last full poll, not this one.
	r.Polling.PullHead = strings.Repeat("b", 40)
	r.Checks = []CheckSummary{{Name: "ci / test", State: "failure", Status: "completed", Conclusion: "failure", ID: 1, Completed: "2026-10-25T22:00:00Z"}}
	e.f.Pull.Head.SHA, e.f.Pull.Base.SHA = head, newBase
	e.f.Checks, e.f.Runs = []Check{actionsCheck(Check{ID: 2, Name: "ci / test", Status: "queued"})}, nil
	e.write()

	task, err := e.s.prepareReviewTask(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	// The task line the reviewer reads first must name the same base as the
	// context file, so an assignment started from it cannot diff from the old one.
	if !strings.Contains(task, `"base":"`+newBase+`"`) || strings.Contains(task, `"base":"`+oldBase+`"`) {
		t.Fatalf("task assignment does not name the current base %s:\n%s", newBase, task)
	}
	paths, err := filepath.Glob(filepath.Join(e.s.Store.Root, "review-inputs", "*", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("context paths: %+v, %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var input ReviewContext
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if input.Assignment.Base != newBase {
		t.Fatalf("assignment base = %s, want the current base %s", input.Assignment.Base, newBase)
	}
	if input.Assignment.Head != head {
		t.Fatalf("assignment head = %s, want the assigned head %s", input.Assignment.Head, head)
	}
	if !strings.Contains(input.DiffStat, "pr.txt") || strings.Contains(input.DiffStat, "merged.txt") {
		t.Fatalf("diff stat = %q, want only the PR commit", input.DiffStat)
	}
	patch, err := os.ReadFile(input.DiffPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "pr.txt") || strings.Contains(string(patch), "merged.txt") {
		t.Fatalf("patch = %q, want only the PR commit", patch)
	}
	if len(input.GitHubChecks) != 1 || input.GitHubChecks[0].ID != 2 || input.GitHubChecks[0].State != "pending" {
		t.Fatalf("checks = %+v, want the pending check read for the assigned head", input.GitHubChecks)
	}
}

func TestReviewInputOmitsGeneratedFiles(t *testing.T) {
	e := newEnv(t)
	repo := t.TempDir()
	testGit(t, repo, "init", "-q", "-b", "main")
	testGit(t, repo, "config", "user.name", "test")
	testGit(t, repo, "config", "user.email", "test@example.com")
	testCommit(t, repo, "base.txt", "base\n", "base")
	base := testGit(t, repo, "rev-parse", "HEAD")
	testGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.MkdirAll(filepath.Join(repo, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	testCommit(t, repo, ".gitattributes", "generated/** linguist-generated\nmanual.txt -diff\nplain.txt linguist-generated=false\n", "mark generated files")
	testCommit(t, repo, "generated/one.txt", "one\ntwo\nthree\n", "add generated preset")
	testCommit(t, repo, "manual.txt", "manual\nlines\n", "add generated manual")
	testCommit(t, repo, "plain.txt", "plain\n", "add reviewed source")
	head := testGit(t, repo, "rev-parse", "HEAD")
	// Attributes must come from the assigned head, not the current checkout.
	testGit(t, repo, "checkout", "-q", "main")
	r := Record{Kind: "local", Key: "local-generated", Repo: "local", RepoPath: repo, Base: base, Head: head, Round: 1}
	task, err := e.s.prepareReviewTask(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(e.s.Store.Root, "review-inputs", "*", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("context paths: %+v, %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		DiffPath       string `json:"diffPath"`
		DiffStat       string `json:"diffStat"`
		GeneratedFiles []struct {
			Path    string `json:"path"`
			Added   int    `json:"added"`
			Deleted int    `json:"deleted"`
		} `json:"generatedFiles"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.GeneratedFiles) != 2 {
		t.Fatalf("generated files = %+v, want two entries with line counts", input.GeneratedFiles)
	}
	if got := input.GeneratedFiles[0]; got.Path != "generated/one.txt" || got.Added != 3 || got.Deleted != 0 {
		t.Fatalf("first generated file = %+v", got)
	}
	if got := input.GeneratedFiles[1]; got.Path != "manual.txt" || got.Added != 2 || got.Deleted != 0 {
		t.Fatalf("second generated file = %+v", got)
	}
	patch, err := os.ReadFile(input.DiffPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"generated/one.txt", "manual.txt"} {
		if strings.Contains(string(patch), "diff --git a/"+path+" ") || strings.Contains(input.DiffStat, path) {
			t.Errorf("generated path %q remains in reviewer diff or stat", path)
		}
	}
	if !strings.Contains(string(patch), "plain.txt") || !strings.Contains(input.DiffStat, "plain.txt") {
		t.Errorf("linguist-generated=false file was omitted: %s", patch)
	}
	if !strings.Contains(task, "generated files") || !strings.Contains(task, "line counts") {
		t.Errorf("task does not explain omitted generated files: %s", task)
	}
}

func TestReviewDeltaOmitsGeneratedFiles(t *testing.T) {
	// One topology per delta branch: new commits on the same base, a rebase
	// whose amended tip makes range-diff print a commit interdiff, and a revert
	// that restores a generated file to its base contents after an earlier round.
	const (
		newCommits = iota
		rebasedAmend
		revertedGenerated
	)
	for name, mode := range map[string]int{"ordinary": newCommits, "rebase": rebasedAmend, "revert": revertedGenerated} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			repo := t.TempDir()
			testGit(t, repo, "init", "-q", "-b", "main")
			testGit(t, repo, "config", "user.name", "test")
			testGit(t, repo, "config", "user.email", "test@example.com")
			testCommit(t, repo, "base.txt", "base\n", "base")
			testGit(t, repo, "checkout", "-q", "-b", "feature")
			if err := os.MkdirAll(filepath.Join(repo, "generated"), 0700); err != nil {
				t.Fatal(err)
			}
			testCommit(t, repo, ".gitattributes", "generated/** linguist-generated\n", "mark generated files")
			// range-diff pairs an amended commit only when its patch clearly beats
			// the fixed cost of treating it as new, so the source file stays large.
			srcBefore := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
			srcAfter := srcBefore + "NEW LINE\n"
			genBefore := "gen one\ngen two\ngen three\n"
			genAfter := "gen CHANGED\ngen two\ngen three\n"
			testCommit(t, repo, "generated/one.txt", genBefore, "add generated preset")
			testCommit(t, repo, "src.txt", srcBefore, "add reviewed source")
			previous := testGit(t, repo, "rev-parse", "HEAD")
			switch mode {
			case rebasedAmend:
				testGit(t, repo, "checkout", "-q", "main")
				testCommit(t, repo, "merged.txt", "a base commit merged after the fork\n", "merged on main")
				testGit(t, repo, "checkout", "-q", "feature")
				testGit(t, repo, "rebase", "-q", "main")
				// Amend the tip so range-diff pairs it with the previous head's tip
				// and prints their interdiff; range-diff shows no patch for new commits.
				for path, body := range map[string]string{"generated/one.txt": genAfter, "src.txt": srcAfter} {
					if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				testGit(t, repo, "add", "-A")
				testGit(t, repo, "commit", "-q", "--amend", "--no-edit")
			case revertedGenerated:
				// The base-to-head diff no longer lists the generated file, so
				// only paths from the delta itself can exclude the large revert.
				testGit(t, repo, "rm", "-q", "generated/one.txt")
				testCommit(t, repo, "src.txt", srcAfter, "change reviewed source")
			default:
				testCommit(t, repo, "generated/one.txt", genAfter, "change generated preset")
				testCommit(t, repo, "src.txt", srcAfter, "change reviewed source")
			}
			head := testGit(t, repo, "rev-parse", "HEAD")
			base := testGit(t, repo, "rev-parse", "main")
			r := Record{Kind: "local", Key: "local-delta-" + name, Repo: "local", RepoPath: repo, Base: base, Head: head, Round: 2,
				ReviewRounds: []state.ReviewRound{{Head: previous}}}
			task, err := e.s.prepareReviewTask(context.Background(), r, "")
			if err != nil {
				t.Fatal(err)
			}
			paths, err := filepath.Glob(filepath.Join(e.s.Store.Root, "review-inputs", "*", "*.json"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("context paths: %+v, %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			var input struct {
				DeltaPath string `json:"deltaPath"`
			}
			if err := json.Unmarshal(data, &input); err != nil {
				t.Fatal(err)
			}
			if input.DeltaPath == "" {
				t.Fatalf("task has no delta:\n%s", task)
			}
			delta, err := os.ReadFile(input.DeltaPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(delta), "generated/one.txt") {
				t.Errorf("generated file remains in the %s delta:\n%s", name, delta)
			}
			if !strings.Contains(string(delta), "src.txt") {
				t.Errorf("reviewed source is missing from the %s delta:\n%s", name, delta)
			}
		})
	}
}
