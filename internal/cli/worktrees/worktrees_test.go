package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/worktrees"
)

func TestListCheckAndRemoveModified(t *testing.T) {
	serveWorktreeEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := commandRepo(t)
	rec := commandWorktree(t, repo, "wf_cli-worktrees")

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--run", rec.RunID, "--check", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var listed []listedWorktree
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatalf("list output = %q: %v", out.String(), err)
	}
	if len(listed) != 1 || listed[0].Path != rec.Path || listed[0].State != worktrees.StateUnchanged {
		t.Fatalf("listed = %#v", listed)
	}

	if err := os.WriteFile(filepath.Join(rec.Path, "post-retention.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = NewCommand()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"remove", filepath.Base(rec.Path)})
	if err := cmd.Execute(); err == nil || !strings.Contains(out.String(), "retained") || !strings.Contains(out.String(), "modified since retention") {
		t.Fatalf("safe remove = %v, %q", err, out.String())
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Fatalf("safe remove deleted modified tree: %v", err)
	}

	cmd = NewCommand()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"rm", rec.Path, "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec.Path); !os.IsNotExist(err) {
		t.Fatalf("forced remove retained tree: %v", err)
	}
}

func TestListCheckContinuesPastDamagedEntries(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	repo := commandRepo(t)
	valid := commandWorktree(t, repo, "wf_list-check")
	uninspectablePath, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(uninspectablePath) })
	uninspectable := worktrees.Record{Path: uninspectablePath, RunID: "wf_list-check", Repo: repo, Head: valid.Head, RetainedAt: time.Now()}
	if err := worktrees.Track(uninspectable); err != nil {
		t.Fatal(err)
	}
	cmd := NewCommand()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--run", "wf_list-check", "--check", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var listed []listedWorktree
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatalf("list output = %q: %v", out.String(), err)
	}
	states := map[string]string{}
	for _, item := range listed {
		states[item.Path] = item.State
	}
	if states[valid.Path] != worktrees.StateUnchanged || states[uninspectablePath] != worktrees.StateError {
		t.Fatalf("list states = %#v", states)
	}
	if !strings.Contains(stderr.String(), "inspect "+uninspectablePath) {
		t.Fatalf("list warnings = %q", stderr.String())
	}
}

func TestPruneDryRunPreservesModified(t *testing.T) {
	serveWorktreeEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := commandRepo(t)
	rec := commandWorktree(t, repo, "wf_missing-run")
	if err := os.WriteFile(filepath.Join(rec.Path, "post-retention.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"prune", "--older-than=24h", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skipped "+rec.Path+" (modified)") {
		t.Fatalf("prune output = %q", out.String())
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Fatalf("dry-run removed tree: %v", err)
	}
	if err := worktrees.Remove(rec, true); err != nil {
		t.Fatal(err)
	}
}

func commandRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	commandGit(t, repo, "init")
	commandGit(t, repo, "config", "user.name", "Dyna Test")
	commandGit(t, repo, "config", "user.email", "dyna-test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commandGit(t, repo, "add", "base.txt")
	commandGit(t, repo, "commit", "-m", "base")
	return repo
}

func commandWorktree(t *testing.T, repo, runID string) worktrees.Record {
	t.Helper()
	path, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	commandGit(t, repo, "worktree", "add", "--detach", path, "HEAD")
	head := commandGit(t, path, "rev-parse", "HEAD")
	rec := worktrees.Record{Path: path, RunID: runID, AgentID: 1, Label: "builder", Repo: repo, Base: head, Head: head, RetainedAt: time.Now()}
	if err := worktrees.Track(rec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "git", "-C", repo, "worktree", "remove", "--force", path).Run()
		_ = os.RemoveAll(path)
	})
	return rec
}

func commandGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestListOrphaned(t *testing.T) {
	storetest.Isolate(t)
	repo := commandRepo(t)
	commandGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	rec := commandWorktree(t, repo, "")
	rec.Session = "closed"
	if err := worktrees.Track(rec); err != nil {
		t.Fatal(err)
	}
	recordtest.WriteRaw(t, runstore.SessionsDir(), "closed", []byte(`{"closed":"2026-01-01T00:00:00Z"}`))
	if err := os.WriteFile(filepath.Join(rec.Path, "untracked"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, jsonOutput := range []bool{true, false} {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		args := []string{"list", "--repo", repo, "--orphaned"}
		if jsonOutput {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if jsonOutput {
			var trees []worktrees.Tree
			if err := json.Unmarshal(out.Bytes(), &trees); err != nil {
				t.Fatal(err)
			}
			if len(trees) != 1 || trees[0].Path != rec.Path {
				t.Fatalf("orphans: %+v", trees)
			}
		} else if !strings.Contains(out.String(), rec.Path) {
			t.Fatalf("output: %s", out.String())
		}
	}
	for _, flag := range []string{"--check", "--run=wf_test"} {
		cmd := NewCommand()
		cmd.SetArgs([]string{"list", "--orphaned", flag})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %s", flag)
		}
	}
}

func TestNewPrintsOnlyPathOnStdout(t *testing.T) {
	serveWorktreeEngineAs(t, authority.Actor{Role: authority.Session, Session: "verified-session"})
	t.Setenv("DYNA_SESSION", "spoofed-session")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := commandRepo(t)
	remote := t.TempDir()
	commandGit(t, remote, "init", "--bare", "--initial-branch=trunk")
	commandGit(t, repo, "remote", "add", "origin", remote)
	commandGit(t, repo, "push", "origin", "HEAD:trunk")
	commandGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")

	cmd := NewCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"new", "feat/ports-cli", "--repo", repo, "--no-deps"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("new: %v: %s", err, stderr.String())
	}
	path := strings.TrimSuffix(stdout.String(), "\n")
	if path == "" || strings.Contains(path, "\n") {
		t.Fatalf("stdout = %q; want the worktree path alone", stdout.String())
	}
	stored, err := worktrees.Get(path)
	if err != nil || stored.Session != "verified-session" || stored.Base != "origin/trunk" {
		t.Fatalf("new worktree = %+v, %v", stored, err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q; want no output with --no-deps", stderr.String())
	}
}

func TestEnvPrintsPortExports(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := commandRepo(t)
	rec := commandWorktree(t, repo, "wf_env")
	got, err := worktrees.Get(rec.Path)
	if err != nil {
		t.Fatal(err)
	}

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"env", rec.Path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("export DYNA_PORT=%d\nexport DYNA_PORTS=%d-%d\nexport PORT=%d\n", got.Port, got.Port, got.Port+9, got.Port)
	if out.String() != want {
		t.Fatalf("env output = %q; want %q", out.String(), want)
	}
}

func TestEnvRejectsUnregisteredTree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cmd := NewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"env", t.TempDir()})
	if err := cmd.Execute(); err == nil {
		t.Fatal("env accepted a directory that is not a registered worktree")
	}
}

func TestListShowsPort(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := commandRepo(t)
	rec := commandWorktree(t, repo, "wf_list-port")
	got, err := worktrees.Get(rec.Path)
	if err != nil {
		t.Fatal(err)
	}

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--run", rec.RunID})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("port %d", got.Port); !strings.Contains(out.String(), want) {
		t.Fatalf("list output = %q; want %q", out.String(), want)
	}
}

func TestEnvFindsRecordWhenRootIsSymlinked(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := commandRepo(t)
	// Git worktree without a record: only the symlink path is registered.
	real, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(real) })
	commandGit(t, repo, "worktree", "add", "--detach", real, "HEAD")
	sub := filepath.Join(real, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Record the worktree under a symlink, as a symlinked worktrees.root does.
	link := filepath.Join(os.TempDir(), "dyna-wt-link-"+filepath.Base(real))
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(link) })
	if err := worktrees.Track(worktrees.Record{Path: link, RunID: "wf_env-symlink"}); err != nil {
		t.Fatal(err)
	}
	got, err := worktrees.Get(link)
	if err != nil {
		t.Fatal(err)
	}

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"env", sub})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("env: %v", err)
	}
	if want := fmt.Sprintf("export PORT=%d\n", got.Port); !strings.Contains(out.String(), want) {
		t.Fatalf("env output = %q; want %q", out.String(), want)
	}
}
