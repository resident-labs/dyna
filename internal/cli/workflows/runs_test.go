package workflows

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/worktrees"
)

func TestRunsListSessionFilter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_session-one")
	t.Setenv(runstore.SessionEnv, "s1")
	first, err := runstore.Create("first", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Finish("ok", "1", nil)

	t.Setenv("DYNA_RUN_ID", "wf_session-none")
	t.Setenv(runstore.SessionEnv, "")
	second, err := runstore.Create("second", "return 2", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.Finish("ok", "2", nil)

	cmd := NewRunsCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"list", "--json", "--session", "s1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var runs []runstore.Meta
	if err := json.Unmarshal(stdout.Bytes(), &runs); err != nil {
		t.Fatalf("decode filtered list: %v\n%s", err, stdout.String())
	}
	if len(runs) != 1 || runs[0].ID != first.Meta.ID || runs[0].Session != "s1" {
		t.Fatalf("filtered runs = %#v", runs)
	}
}

func TestRunsRemoveWorktreeDispositions(t *testing.T) {
	serveRunEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := lifecycleRepo(t)
	run := finishedRun(t, "wf_remove-worktrees")
	unchanged := lifecycleWorktree(t, repo, run.Meta.ID)
	modified := lifecycleWorktree(t, repo, run.Meta.ID)
	if err := os.WriteFile(filepath.Join(modified.Path, "post-retention.txt"), []byte("survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRunsCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"remove", run.Meta.ID, "--worktrees=remove-unchanged"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.Dir); !os.IsNotExist(err) {
		t.Fatalf("run directory still exists: %v", err)
	}
	if _, err := os.Stat(unchanged.Path); !os.IsNotExist(err) {
		t.Fatalf("unchanged worktree survived remove-unchanged: %v", err)
	}
	if _, err := os.Stat(modified.Path); err != nil {
		t.Fatalf("modified worktree removed by remove-unchanged: %v", err)
	}
	if !strings.Contains(out.String(), "removed worktree "+unchanged.Path) || !strings.Contains(out.String(), "skipped modified worktree "+modified.Path) {
		t.Fatalf("remove output = %q", out.String())
	}
	if err := worktrees.Remove(modified, true); err != nil {
		t.Fatal(err)
	}

	retainedRun := finishedRun(t, "wf_retain-worktree")
	retained := lifecycleWorktree(t, repo, retainedRun.Meta.ID)
	cmd = NewRunsCommand()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"remove", retainedRun.Meta.ID})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := worktrees.Get(retained.Path); err != nil {
		t.Fatalf("default deletion dropped worktree registry: %v", err)
	}
	if !strings.Contains(out.String(), "retained worktree "+retained.Path) || !strings.Contains(out.String(), "dyna git worktrees list") {
		t.Fatalf("retain output = %q", out.String())
	}
	if err := worktrees.Remove(retained, true); err != nil {
		t.Fatal(err)
	}

	removeAllRun := finishedRun(t, "wf_remove-all-worktree")
	removeAll := lifecycleWorktree(t, repo, removeAllRun.Meta.ID)
	if err := os.WriteFile(filepath.Join(removeAll.Path, "changed.txt"), []byte("remove explicitly\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = NewRunsCommand()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"remove", removeAllRun.Meta.ID, "--worktrees=remove-all"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removeAll.Path); !os.IsNotExist(err) {
		t.Fatalf("modified worktree survived remove-all: %v", err)
	}
}

func TestRunsRemoveRejectsRetiredWorktreeDispositions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, retired := range []string{"preserve", "auto", "all"} {
		run := finishedRun(t, "wf_reject-"+retired)
		cmd := NewRunsCommand()
		cmd.SetArgs([]string{"remove", run.Meta.ID, "--worktrees=" + retired})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "retain, remove-unchanged, remove-all") {
			t.Fatalf("--worktrees=%s error = %v", retired, err)
		}
		if _, err := os.Stat(run.Dir); err != nil {
			t.Fatalf("invalid disposition removed run %s: %v", run.Meta.ID, err)
		}
	}
}

func TestRunsRemoveSkipsCorruptRegistryRecord(t *testing.T) {
	serveRunEngine(t)
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	run := finishedRun(t, "wf_remove-corrupt-registry")
	registryDir := filepath.Join(data, "dyna", "worktrees")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryDir, "dyna-wt-corrupt.json"), []byte(`{not-json`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRunsCommand()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"remove", run.Meta.ID})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.Dir); !os.IsNotExist(err) {
		t.Fatalf("corrupt registry blocked run deletion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(registryDir, "dyna-wt-corrupt.json")); err != nil {
		t.Fatalf("damaged legacy record was removed: %v", err)
	}
}

func TestRunsClearDefaultsToRetain(t *testing.T) {
	serveRunEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := lifecycleRepo(t)
	run := finishedRun(t, "wf_clear-retain")
	rec := lifecycleWorktree(t, repo, run.Meta.ID)
	cmd := NewRunsCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"clear"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := worktrees.Get(rec.Path); err != nil {
		t.Fatalf("clear default dropped retained worktree: %v", err)
	}
	if !strings.Contains(out.String(), "retained worktree "+rec.Path) || !strings.Contains(out.String(), "dyna git worktrees list") {
		t.Fatalf("clear retain output = %q", out.String())
	}
	if err := worktrees.Remove(rec, true); err != nil {
		t.Fatal(err)
	}
}

func TestRunsClearHonorsRemoveUnchanged(t *testing.T) {
	serveRunEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := lifecycleRepo(t)
	run := finishedRun(t, "wf_clear-worktrees")
	rec := lifecycleWorktree(t, repo, run.Meta.ID)
	cmd := NewRunsCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"clear", "--worktrees=remove-unchanged"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec.Path); !os.IsNotExist(err) {
		t.Fatalf("clear remove-unchanged retained unchanged worktree: %v", err)
	}
	if _, err := os.Stat(run.Dir); !os.IsNotExist(err) {
		t.Fatalf("clear kept run directory: %v", err)
	}
	if !strings.Contains(out.String(), "removed 1 run(s)") {
		t.Fatalf("clear output = %q", out.String())
	}
}

func TestRunsClearContinuesAfterPerRunWorktreeFailure(t *testing.T) {
	serveRunEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	badRun := finishedRun(t, "wf_clear-bad-worktree")
	goodRun := finishedRun(t, "wf_clear-good")
	path, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(path) })
	rec := worktrees.Record{Path: path, RunID: badRun.Meta.ID, Repo: t.TempDir(), Head: "head", RetainedAt: time.Now()}
	if err := worktrees.Track(rec); err != nil {
		t.Fatal(err)
	}

	cmd := NewRunsCommand()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"clear", "--worktrees=remove-all"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), badRun.Meta.ID) {
		t.Fatalf("clear must report the failed removal: %v", err)
	}
	if _, err := os.Stat(goodRun.Dir); !os.IsNotExist(err) {
		t.Fatalf("unrelated completed run survived clear: %v", err)
	}
	if _, err := os.Stat(badRun.Dir); err != nil {
		t.Fatalf("run with failed worktree removal was deleted: %v", err)
	}
	if !strings.Contains(out.String(), "removed 1 run(s)") || !strings.Contains(stderr.String(), "warning: remove run "+badRun.Meta.ID) {
		t.Fatalf("clear output = %q; warnings = %q", out.String(), stderr.String())
	}
}

func finishedRun(t *testing.T, id string) *runstore.Run {
	t.Helper()
	t.Setenv("DYNA_RUN_ID", id)
	run, err := runstore.Create(id, "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	return run
}

func lifecycleRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	lifecycleGit(t, repo, "init")
	lifecycleGit(t, repo, "config", "user.name", "Dyna Test")
	lifecycleGit(t, repo, "config", "user.email", "dyna-test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lifecycleGit(t, repo, "add", "base.txt")
	lifecycleGit(t, repo, "commit", "-m", "base")
	return repo
}

func lifecycleWorktree(t *testing.T, repo, runID string) worktrees.Record {
	t.Helper()
	path, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	lifecycleGit(t, repo, "worktree", "add", "--detach", path, "HEAD")
	rec := worktrees.Record{Path: path, RunID: runID, Repo: repo, Base: lifecycleGit(t, path, "rev-parse", "HEAD"), Head: lifecycleGit(t, path, "rev-parse", "HEAD"), RetainedAt: time.Now()}
	if err := worktrees.Track(rec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", path).Run()
		_ = os.RemoveAll(path)
	})
	return rec
}

func lifecycleGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunAndDemoAreRefusedInWorkflowMode(t *testing.T) {
	inRepo(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_MODE", "workflow")
	script := filepath.Join(t.TempDir(), "w.js")
	if err := os.WriteFile(script, []byte(`return 1`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{script}, {script, "--detach"}, {script, "--resume", "wf_x"}} {
		cmd := NewRunCommand()
		cmd.SetArgs(args)
		cmd.SilenceUsage = true
		err := cmd.Execute()
		if err == nil || err.Error() != "run is owner or root only; dyna run is for room roots. Start a child with dyna subagent start, or post the job to @root." {
			t.Fatalf("dyna run %v err = %v", args, err)
		}
	}
	demo := NewDemoCommand()
	if err := demo.Execute(); err == nil || err.Error() != "demo is owner or root or session only; use subagent mode to start work" {
		t.Fatalf("dyna demo err = %v", err)
	}
	// Planning starts no workers and stays available.
	plan := NewRunCommand()
	plan.SetArgs([]string{script, "--plan"})
	plan.SetOut(io.Discard)
	if err := plan.Execute(); err != nil {
		t.Fatalf("dyna run --plan refused in workflow mode: %v", err)
	}
}

func TestReadOnlyRunsCommandsWorkInWorkflowMode(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_MODE", "workflow")
	cmd := NewRunsCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("runs list refused in workflow mode: %v", err)
	}
}
