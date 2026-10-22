package gitsvc_test

import (
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/gitsvc"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitBrowseHeaderAndVisibleRows(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "old\nname.txt", "one\n")
	writeWorkspace(t, root, ":(glob)*.txt", "literal\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First (#123)")
	writeWorkspace(t, root, "old\nname.txt", "two\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Second")
	writeWorkspace(t, root, "untracked", "new\n")
	status, err := e.git.GitStatus(rec.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.CommitCount != 2 || status.ChangedFiles != 1 || status.Commits[1].PRNumber != 123 {
		t.Fatalf("header: %+v", status)
	}
	rows, err := e.git.GitLastCommits(rec.ID, "", []string{"old\nname.txt", ":(glob)*.txt", "untracked"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Commits) != 2 || rows.Commits["old\nname.txt"].Subject != "Second" || rows.Commits[":(glob)*.txt"].Subject != "First (#123)" {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestGitVisibleRowsLimits(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "file.txt", "one\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Initial")
	if _, err := e.git.GitLastCommits(rec.ID, "", make([]string, 101)); err == nil {
		t.Fatal("accepted too many paths")
	}
	e.git.MaxBytes = 20
	rows, err := e.git.GitLastCommits(rec.ID, "", []string{"file.txt"})
	if err != nil || !rows.Truncated || len(rows.Commits) != 0 {
		t.Fatalf("output limit: %+v %v", rows, err)
	}
}

// BenchmarkGitLastCommits measures the complete visible-row request on this repo.
func BenchmarkGitLastCommits(b *testing.B) {
	rootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		b.Fatal(err)
	}
	root := strings.TrimSpace(string(rootBytes))
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	data, err := cmd.Output()
	if err != nil {
		b.Fatal(err)
	}
	paths := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	b.Logf("repository has %d tracked paths", len(paths))
	if len(paths) > 50 {
		paths = paths[:50]
	}
	svc := &gitsvc.Service{WorkspaceRoot: func(string, string) (string, error) { return root, nil }, MaxBytes: filesvc.MaxWorkspaceBytes}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.GitLastCommits("", "", paths); err != nil {
			b.Fatal(err)
		}
	}
}

func TestGitVisibleRowsCommitLimit(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	var input strings.Builder
	for i := 0; i < 1001; i++ {
		path := "other.txt"
		if i == 0 {
			path = "old.txt"
		}
		fmt.Fprintf(&input, "commit refs/heads/main\ncommitter Test <test@example.test> %d +0000\ndata 6\ncommit\nM 100644 inline %s\ndata 5\n%04d\n\n", 1700000000+i, path, i)
	}
	cmd := exec.Command("git", "-c", "core.fsmonitor=", "fast-import", "--quiet")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(input.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %s %v", output, err)
	}
	rows, err := e.git.GitLastCommits(rec.ID, "", []string{"."})
	if err != nil || rows.Commits["."].Subject != "commit" {
		t.Fatalf("directory: %+v %v", rows, err)
	}
	// Path filtering would skip unchanged commits, so use the directory row to
	// include every commit while leaving the old file unresolved.
	rows, err = e.git.GitLastCommits(rec.ID, "", []string{"old.txt", "."})
	if err != nil || !rows.Truncated || len(rows.Commits) != 1 {
		t.Fatalf("commit limit: %+v %v", rows, err)
	}
}

func TestGitVisibleRowsParentAndUnborn(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	nested := filepath.Join(root, "sub")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	svc := &gitsvc.Service{WorkspaceRoot: func(string, string) (string, error) { return nested, nil }, MaxBytes: filesvc.MaxWorkspaceBytes}
	writeWorkspace(t, root, "parent.txt", "one\n")
	rows, err := svc.GitLastCommits("", "", []string{"../parent.txt"})
	if err != nil || len(rows.Commits) != 0 {
		t.Fatalf("unborn: %+v %v", rows, err)
	}
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	absolute := filepath.Join(root, "parent.txt")
	for _, path := range []string{"../parent.txt", absolute} {
		rows, err = svc.GitLastCommits("", "", []string{path})
		if err != nil || rows.Commits[path].Subject != "First" {
			t.Fatalf("path %q: %+v %v", path, rows, err)
		}
	}
	svc.WorkspaceRoot = func(string, string) (string, error) { return t.TempDir(), nil }
	if _, err := svc.GitLastCommits("", "", []string{"file"}); err == nil {
		t.Fatal("hid non-repository error")
	}
}
