package gitsvc_test

import (
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/gitsvc"
	"dyna-agent/internal/subagent"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.fsmonitor=", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, b, err)
	}
	return string(b)
}

func TestGitStatusDiffRenameAndUntracked(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "old name.txt", "original\n")
	writeWorkspace(t, root, "dual.txt", "one\n")
	writeWorkspace(t, root, "binary.dat", "old\x00bytes")
	writeWorkspace(t, root, ":(glob)*.txt", "literal\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Initial commit")
	gitTest(t, root, "mv", "old name.txt", "new\nname.txt")
	writeWorkspace(t, root, "dual.txt", "staged\n")
	gitTest(t, root, "add", "dual.txt")
	writeWorkspace(t, root, "dual.txt", "working\n")
	writeWorkspace(t, root, "binary.dat", "new\x00bytes")
	writeWorkspace(t, root, "untracked.txt", "hello\nworld")
	writeWorkspace(t, root, ":(glob)*.txt", "literal changed\n")
	status, err := e.git.GitStatus(rec.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" || len(status.Commits) != 1 || status.Commits[0].Subject != "Initial commit" {
		t.Fatalf("status: %+v", status)
	}
	files := map[string]gitsvc.GitFileStatus{}
	for _, f := range status.Files {
		files[f.Path] = f
	}
	if f := files["new\nname.txt"]; f.OriginalPath != "old name.txt" || f.Status != "R" || !f.Staged {
		t.Fatalf("rename: %+v", f)
	}
	if f := files["dual.txt"]; !f.Staged || !f.Unstaged || f.IndexStatus != "M" || f.WorktreeStatus != "M" {
		t.Fatalf("dual: %+v", f)
	}
	if !files["untracked.txt"].Untracked {
		t.Fatalf("untracked: %+v", files)
	}
	for _, tc := range []struct {
		path   string
		staged bool
		want   string
	}{
		{"dual.txt", false, "+working"}, {"dual.txt", true, "+staged"}, {"new\nname.txt", true, "new file mode"}, {"binary.dat", false, "Binary files"}, {"untracked.txt", false, "+hello"}, {":(glob)*.txt", false, "+literal changed"}, {"", false, "+working"},
	} {
		diff, err := e.git.GitDiff(rec.ID, "", tc.path, tc.staged)
		if err != nil || !strings.Contains(diff.Text, tc.want) {
			t.Fatalf("diff %q staged=%v: %q err=%v", tc.path, tc.staged, diff.Text, err)
		}
		if tc.path == ":(glob)*.txt" && strings.Contains(diff.Text, "working") {
			t.Fatalf("pathspec pattern was expanded: %s", diff.Text)
		}
	}
	// Caller environment cannot redirect status to another repository.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
	if _, err := e.git.GitStatus(rec.ID, ""); err != nil {
		t.Fatalf("inherited GIT_DIR: %v", err)
	}
}

func TestGitEmptyDetachedAndSessionWorktree(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	status, err := e.git.GitStatus(rec.ID, "")
	if err != nil || len(status.Commits) != 0 || status.OID != "(initial)" {
		t.Fatalf("unborn: %+v %v", status, err)
	}
	writeWorkspace(t, root, "a.txt", "initial\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	session := e.start(rec, "worker")
	gitTest(t, root, "worktree", "add", "--detach", session.Dir)
	writeWorkspace(t, session.Dir, "a.txt", "worktree change\n")
	status, err = e.git.GitStatus(rec.ID, session.ID)
	if err != nil || status.Branch != "(detached)" || len(status.Files) != 1 {
		t.Fatalf("worktree status: %+v %v", status, err)
	}
	diff, err := e.git.GitDiff(rec.ID, session.ID, "a.txt", false)
	if err != nil || !strings.Contains(diff.Text, "+worktree change") {
		t.Fatalf("worktree diff: %+v %v", diff, err)
	}
	status, err = e.git.GitStatus(rec.ID, "")
	if err != nil || len(status.Files) != 0 {
		t.Fatalf("room changed: %+v %v", status, err)
	}
}

func TestGitDiffCapAndNoExternalDrivers(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "big.txt", "old\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	writeWorkspace(t, root, "big.txt", strings.Repeat("new line\n", filesvc.MaxWorkspaceBytes/4))
	diff, err := e.git.GitDiff(rec.ID, "", "big.txt", false)
	if err != nil || !diff.Truncated || len(diff.Text) > filesvc.MaxWorkspaceBytes {
		t.Fatalf("cap: len=%d truncated=%v err=%v", len(diff.Text), diff.Truncated, err)
	}
	writeWorkspace(t, root, ".gitattributes", "*.txt diff=bad\n")
	gitTest(t, root, "config", "diff.bad.command", "/does/not/exist")
	gitTest(t, root, "config", "diff.bad.textconv", "/does/not/exist")
	gitTest(t, root, "config", "core.fsmonitor", "/does/not/exist")
	if _, err := e.git.GitDiff(rec.ID, "", "big.txt", false); err != nil {
		t.Fatalf("external driver invoked: %v", err)
	}
}

func TestGitStatusSubdirectoryPathsAndUpstream(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	writeWorkspace(t, root, "outer.txt", "initial\n")
	writeWorkspace(t, root, "sub/inner.txt", "initial\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Initial")
	gitTest(t, root, "branch", "base")
	gitTest(t, root, "config", "branch.main.remote", ".")
	gitTest(t, root, "config", "branch.main.merge", "refs/heads/base")
	writeWorkspace(t, root, "outer.txt", "next\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Next")
	writeWorkspace(t, root, "outer.txt", "outer change\n")
	writeWorkspace(t, root, "sub/inner.txt", "inner change\n")
	writeWorkspace(t, root, "sub/untracked.txt", "new file\n")
	session := e.start(rec, "worker")
	_, err := e.svc.Notify.Sessions.Store.UpdateMeta(session.ID, func(meta *subagent.Meta) { meta.Dir = filepath.Join(root, "sub") })
	if err != nil {
		t.Fatal(err)
	}
	status, err := e.git.GitStatus(rec.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Ahead != 1 || status.Behind != 0 || status.Upstream != "base" || len(status.Commits) != 2 {
		t.Fatalf("branch: %+v", status)
	}
	paths := map[string]bool{}
	for _, f := range status.Files {
		paths[f.Path] = true
	}
	if !paths["../outer.txt"] || !paths["inner.txt"] || !paths["untracked.txt"] {
		t.Fatalf("paths: %+v", status.Files)
	}
	file, err := e.files.ReadFile(rec.ID, session.ID, "../outer.txt")
	if err != nil || file.Text != "outer change\n" {
		t.Fatalf("nested read: %+v %v", file, err)
	}
	diff, err := e.git.GitDiff(rec.ID, session.ID, "../outer.txt", false)
	if err != nil || !strings.Contains(diff.Text, "+outer change") {
		t.Fatalf("nested diff: %+v %v", diff, err)
	}
	diff, err = e.git.GitDiff(rec.ID, session.ID, filepath.Join(root, "sub/untracked.txt"), false)
	if err != nil || !strings.Contains(diff.Text, "+new file") {
		t.Fatalf("absolute untracked: %+v %v", diff, err)
	}
}

// A user's diff.mnemonicPrefix or diff.noprefix must not change the a/ and
// b/ headers the clients parse, for tracked, staged and untracked diffs.
func TestGitDiffPrefixesIgnoreUserConfig(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	gitTest(t, root, "config", "diff.mnemonicPrefix", "true")
	writeWorkspace(t, root, "a.txt", "one\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Initial commit")
	writeWorkspace(t, root, "a.txt", "two\n")
	writeWorkspace(t, root, "new.txt", "hello\n")
	for _, tc := range []struct {
		path   string
		staged bool
		want   string
	}{
		{"a.txt", false, "diff --git a/a.txt b/a.txt"},
		{"new.txt", false, "diff --git a/new.txt b/new.txt"},
	} {
		d, err := e.git.GitDiff(rec.ID, "", tc.path, tc.staged)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(d.Text, tc.want) {
			t.Fatalf("%s: want %q in:\n%s", tc.path, tc.want, d.Text)
		}
	}
	gitTest(t, root, "add", "a.txt")
	gitTest(t, root, "config", "diff.noprefix", "true")
	d, err := e.git.GitDiff(rec.ID, "", "a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Text, "diff --git a/a.txt b/a.txt") {
		t.Fatalf("staged with noprefix:\n%s", d.Text)
	}
}

func TestGitStatusTellsALinkedWorktreeFromTheMainCheckout(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "a.txt", "initial\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	main, err := e.git.GitStatus(rec.ID, "")
	if err != nil || main.Worktree {
		t.Fatalf("main checkout reported as a worktree: %+v %v", main, err)
	}
	session := e.start(rec, "worker")
	gitTest(t, root, "worktree", "add", "--detach", session.Dir)
	linked, err := e.git.GitStatus(rec.ID, session.ID)
	if err != nil || !linked.Worktree {
		t.Fatalf("linked worktree not reported: %+v %v", linked, err)
	}
}
