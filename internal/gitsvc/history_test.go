package gitsvc_test

import (
	"dyna-agent/internal/filesvc"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHistoryRenamePagingAndBlame(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "old.txt", "one\ntwo\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	gitTest(t, root, "mv", "old.txt", "new.txt")
	gitTest(t, root, "commit", "-m", "Moved")
	writeWorkspace(t, root, "new.txt", "one\nchanged\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Edit")
	page, err := e.git.GitHistory(rec.ID, "", "new.txt", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Commits) != 1 || page.Commits[0].Subject != "Edit" || page.Commits[0].Added != 1 || page.Commits[0].Deleted != 1 || !page.Earlier {
		t.Fatalf("page: %+v", page)
	}
	page, err = e.git.GitHistory(rec.ID, "", "new.txt", page.Cursor, 1)
	if err != nil || len(page.Commits) != 1 || !page.Commits[0].Moved || page.Commits[0].Subject != "Moved" {
		t.Fatalf("rename page: %+v %v", page, err)
	}
	page, err = e.git.GitHistory(rec.ID, "", "new.txt", page.Cursor, 1)
	if err != nil || len(page.Commits) != 1 || page.Commits[0].Subject != "First" || page.Earlier || page.Cursor != "" {
		t.Fatalf("old page: %+v %v", page, err)
	}
	all, err := e.git.GitHistory(rec.ID, "", "", "", 50)
	if err != nil || len(all.Commits) != 3 {
		t.Fatalf("repo: %+v %v", all, err)
	}
	blame, err := e.git.GitBlame(rec.ID, "", "new.txt")
	if err != nil || len(blame.Ranges) != 2 || blame.Ranges[0].StartLine != 1 || blame.Ranges[0].EndLine != 1 || blame.Ranges[0].Commit.Subject != "First" || blame.Ranges[1].Commit.Subject != "Edit" {
		t.Fatalf("blame: %+v %v", blame, err)
	}
}

func TestGitHistoryAndBlameLimits(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	empty, err := e.git.GitHistory(rec.ID, "", "", "", 50)
	if err != nil || len(empty.Commits) != 0 {
		t.Fatalf("unborn: %+v %v", empty, err)
	}
	var input strings.Builder
	for i := 0; i < 205; i++ {
		fmt.Fprintf(&input, "commit refs/heads/main\ncommitter Test <test@example.test> %d +0000\ndata 6\ncommit\nM 100644 inline a.txt\ndata 5\n%04d\n\n", 1700000000+i, i)
	}
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(input.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %s %v", output, err)
	}
	gitTest(t, root, "reset", "--hard", "HEAD")
	page, err := e.git.GitHistory(rec.ID, "", "", "", 1000)
	if err != nil || len(page.Commits) != 200 || !page.Earlier {
		t.Fatalf("page cap: %d %+v %v", len(page.Commits), page, err)
	}
	for _, cursor := range []string{"invalid!", base64.RawURLEncoding.EncodeToString([]byte(`{"head":"--all","offset":1}`))} {
		if _, err := e.git.GitHistory(rec.ID, "", "", cursor, 1); err == nil {
			t.Fatalf("accepted cursor %q", cursor)
		}
	}
	if _, err := e.git.GitHistory(rec.ID, "", "a.txt", page.Cursor, 1); err == nil {
		t.Fatal("accepted cursor for other path")
	}
	gitTest(t, root, "commit", "--allow-empty", "-m", "New after first page")
	next, err := e.git.GitHistory(rec.ID, "", "", page.Cursor, 200)
	if err != nil || len(next.Commits) != 5 || next.Earlier {
		t.Fatalf("stable page: %+v %v", next, err)
	}
	e.git.MaxBytes = 20
	limited, err := e.git.GitHistory(rec.ID, "", "", "", 50)
	if err != nil || !limited.Truncated || len(limited.Commits) != 0 || limited.Earlier {
		t.Fatalf("history output cap: %+v %v", limited, err)
	}
	blame, err := e.git.GitBlame(rec.ID, "", "a.txt")
	if err != nil || !blame.Truncated || len(blame.Ranges) != 0 {
		t.Fatalf("blame output cap: %+v %v", blame, err)
	}
	writeWorkspace(t, root, "a.txt", strings.Repeat("line\n", 20))
	if _, err := e.git.GitBlame(rec.ID, "", "a.txt"); err == nil {
		t.Fatal("accepted oversized file")
	}
	if _, err := e.git.GitBlame(rec.ID, "", ""); err == nil {
		t.Fatal("accepted empty path")
	}
	writeWorkspace(t, root, "empty.txt", "")
	blame, err = e.git.GitBlame(rec.ID, "", "empty.txt")
	if err != nil || len(blame.Ranges) != 0 {
		t.Fatalf("empty blame: %+v %v", blame, err)
	}
	e.git.MaxBytes = 16 << 20
	writeWorkspace(t, root, "a.txt", strings.Repeat("line\n", 20001))
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Large")
	blame, err = e.git.GitBlame(rec.ID, "", "a.txt")
	if err != nil || !blame.Truncated || len(blame.Ranges) != 1 || blame.Ranges[0].EndLine != 20000 {
		t.Fatalf("line cap: %+v %v", blame, err)
	}
}

func TestGitRepositoryHistoryIncludesMergeParents(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "base.txt", "base\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Base")
	gitTest(t, root, "checkout", "-b", "side")
	writeWorkspace(t, root, "side.txt", "side\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Side")
	gitTest(t, root, "checkout", "main")
	writeWorkspace(t, root, "main.txt", "main\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Main")
	gitTest(t, root, "merge", "--no-ff", "side", "-m", "Merge")
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := e.git.GitHistory(rec.ID, "", "", cursor, 1)
		if err != nil || len(page.Commits) != 1 {
			t.Fatalf("page: %+v %v", page, err)
		}
		commit := page.Commits[0]
		if seen[commit.Subject] {
			t.Fatalf("duplicate commit: %+v", commit)
		}
		seen[commit.Subject] = true
		if commit.Subject == "Merge" && commit.Added != 1 {
			t.Fatalf("merge stats: %+v", commit)
		}
		if !page.Earlier {
			break
		}
		cursor = page.Cursor
	}
	if len(seen) != 4 || !seen["Side"] {
		t.Fatalf("lost merge parent: %v", seen)
	}
}

func TestGitHistoryRenameFromNestedWorkspace(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "old.txt", "line\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	gitTest(t, root, "mv", "old.txt", "new.txt")
	gitTest(t, root, "commit", "-m", "Moved")
	nested := filepath.Join(root, "sub")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	e.git.WorkspaceRoot = func(string, string) (string, error) { return nested, nil }
	page, err := e.git.GitHistory(rec.ID, "", "../new.txt", "", 1)
	if err != nil || len(page.Commits) != 1 || !page.Earlier {
		t.Fatalf("first: %+v %v", page, err)
	}
	page, err = e.git.GitHistory(rec.ID, "", "../new.txt", page.Cursor, 1)
	if err != nil || len(page.Commits) != 1 || page.Commits[0].Subject != "First" {
		t.Fatalf("old name: %+v %v", page, err)
	}
}

func TestGitFileHistoryIncludesMergeParents(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "file.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Base")
	gitTest(t, root, "checkout", "-b", "side")
	writeWorkspace(t, root, "file.txt", "side\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Side")
	gitTest(t, root, "checkout", "main")
	writeWorkspace(t, root, "file.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nmain\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Main")
	gitTest(t, root, "merge", "--no-ff", "side", "-m", "Merge")
	all, err := e.git.GitHistory(rec.ID, "", "file.txt", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := e.git.GitHistory(rec.ID, "", "file.txt", cursor, 1)
		if err != nil || len(page.Commits) != 1 {
			t.Fatalf("page: %+v %v", page, err)
		}
		commit := page.Commits[0]
		if seen[commit.Hash] {
			t.Fatal("duplicate")
		}
		seen[commit.Hash] = true
		if !page.Earlier {
			break
		}
		cursor = page.Cursor
	}
	if len(seen) != len(all.Commits) {
		t.Fatalf("paged %d commits, whole page has %d", len(seen), len(all.Commits))
	}
}

func TestGitReadHistoricalFile(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "old.go", "package old\n")
	writeWorkspace(t, root, "binary.dat", "old\x00bytes")
	imageBytes, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9WQAAAAASUVORK5CYII=")
	writeWorkspace(t, root, "image.png", string(imageBytes))
	writeWorkspace(t, root, "large.txt", strings.Repeat("a", filesvc.MaxWorkspaceBytes-1)+"€end")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	hash := strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD"))
	gitTest(t, root, "mv", "old.go", "new.go")
	gitTest(t, root, "commit", "-m", "Moved")
	writeWorkspace(t, root, "new.go", "package working\n")
	file, err := e.git.GitReadFile(rec.ID, "", hash, "old.go")
	if err != nil || file.Text != "package old\n" || file.Language != "go" || file.Binary || file.Truncated {
		t.Fatalf("historical text: %+v %v", file, err)
	}
	file, err = e.git.GitReadFile(rec.ID, "", hash, "binary.dat")
	if err != nil || !file.Binary || file.Text != "" {
		t.Fatalf("binary: %+v %v", file, err)
	}
	imageFile, imageErr := e.git.GitReadFile(rec.ID, "", hash, "image.png")
	if imageErr != nil || !imageFile.Binary || imageFile.Text != "" || !strings.HasPrefix(imageFile.URL, "data:image/png;base64,") {
		t.Fatalf("historical image: %+v %v", imageFile, imageErr)
	}
	file, err = e.git.GitReadFile(rec.ID, "", hash, "large.txt")
	if err != nil || !file.Truncated || file.Binary || len(file.Text) != filesvc.MaxWorkspaceBytes-1 || file.Size != filesvc.MaxWorkspaceBytes+5 {
		t.Fatalf("large: size=%d text=%d binary=%v truncated=%v err=%v", file.Size, len(file.Text), file.Binary, file.Truncated, err)
	}
	if _, err := e.git.GitReadFile(rec.ID, "", "--all", "old.go"); err == nil {
		t.Fatal("accepted revision option")
	}
	if _, err := e.git.GitReadFile(rec.ID, "", hash, "missing"); err == nil {
		t.Fatal("accepted missing path")
	}
	page, err := e.git.GitHistory(rec.ID, "", "new.go", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	page, err = e.git.GitHistory(rec.ID, "", "new.go", page.Cursor, 50)
	if err != nil || len(page.Commits) != 1 || page.Commits[0].Path != "old.go" {
		t.Fatalf("historical path: %+v %v", page, err)
	}
}

func TestGitReadFileRejectsUnsafePaths(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "a.txt", "one\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "First")
	hash := strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD"))
	for _, path := range []string{"-a.txt", "--help"} {
		if _, err := e.git.GitReadFile(rec.ID, "", hash, path); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	nested := filepath.Join(root, "sub")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	e.git.WorkspaceRoot = func(string, string) (string, error) { return nested, nil }
	for _, path := range []string{"../a.txt", filepath.Join(root, "a.txt")} {
		file, err := e.git.GitReadFile(rec.ID, "", hash, path)
		if err != nil || file.Text != "one\n" {
			t.Fatalf("parent path %q: %+v %v", path, file, err)
		}
	}

	treeHash := strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD^{tree}"))
	if _, err := e.git.GitReadFile(rec.ID, "", treeHash, "../a.txt"); err == nil {
		t.Fatal("accepted tree object as a commit")
	}
	if _, err := e.git.GitReadFile(rec.ID, "", hash, filepath.Join(filepath.Dir(root), "outside")); err == nil || !strings.Contains(err.Error(), "outside this repository") {
		t.Fatalf("outside repository error: %v", err)
	}

}

func TestGitBlameDisablesTextconv(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "file.txt", "one\ntwo\n")
	writeWorkspace(t, root, ".gitattributes", "*.txt diff=preview\n")
	writeWorkspace(t, root, "textconv.sh", "#!/bin/sh\nprintf ran > textconv-ran\nprintf 'converted\\n'\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Original")
	gitTest(t, root, "config", "diff.preview.textconv", "sh ./textconv.sh")
	blame, err := e.git.GitBlame(rec.ID, "", "file.txt")
	if _, markerErr := os.Stat(filepath.Join(root, "textconv-ran")); !os.IsNotExist(markerErr) {
		t.Errorf("textconv driver executed: %v", markerErr)
	}
	if err != nil || len(blame.Ranges) != 1 || blame.Ranges[0].StartLine != 1 || blame.Ranges[0].EndLine != 2 {
		t.Fatalf("original lines: %+v %v", blame, err)
	}
}

func TestGitBlameContentResemblesHeader(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	gitTest(t, root, "init", "-b", "main")
	writeWorkspace(t, root, "file.txt", strings.Repeat("a", 40)+" 1 2\nplain\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "Original")
	hash := strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD"))
	blame, err := e.git.GitBlame(rec.ID, "", "file.txt")
	if err != nil || len(blame.Ranges) != 1 || blame.Ranges[0].StartLine != 1 || blame.Ranges[0].EndLine != 2 || blame.Ranges[0].Commit.Hash != hash {
		t.Fatalf("source header text: %+v %v", blame, err)
	}
}
