package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// diffTestRepo is a real checkout with one commit and a bare origin that holds its branch.
func diffTestRepo(t *testing.T) (root, origin string, repo contract.BoxRepo, git func(...string) string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, origin = t.TempDir(), t.TempDir()
	repo = contract.BoxRepo{Name: "repo", Branch: "v2/s"}
	dir := filepath.Join(root, string(repo.Name))
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	git = func(args ...string) string {
		t.Helper()
		out, err := runGit(context.Background(), dir, "", args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := runGit(context.Background(), origin, "", "init", "--bare"); err != nil {
		t.Fatal(err)
	}
	git("init", "-b", "v2/s")
	git("remote", "add", "origin", origin)
	for name, text := range map[string]string{"tracked.txt": "first", "kept.bin": "first", ".gitignore": "ignored.bin\n"} {
		writeFile(t, filepath.Join(dir, name), text)
	}
	git("add", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "first")
	git("push", "origin", "v2/s")
	return root, origin, repo, git
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSaveDiffHoldsUnpushedCommitsDirtyUntrackedAndBinaryWork(t *testing.T) {
	root, _, repo, git := diffTestRepo(t)
	dir := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "second")
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-am", "an unpushed change\n\nwith a body")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "third, uncommitted")
	writeFile(t, filepath.Join(dir, "new.txt"), "untracked text")
	writeFile(t, filepath.Join(dir, "image.bin"), "PNG\x00\x01\x02")
	writeFile(t, filepath.Join(dir, "ignored.bin"), "SECRET-IN-AN-IGNORED-FILE")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "SECRET-OUTSIDE-THE-TREE")
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "archives")
	headBefore, indexBefore := git("rev-parse", "HEAD"), git("status", "--porcelain")

	got := saveDiff(context.Background(), root, folder, "ses_1", "my session", []contract.BoxRepo{repo}, runGit)

	want := filepath.Join(folder, "my_session-ses_1.diff")
	if got.File != want || got.Error != "" || got.Commits != 1 || got.Files != 4 || got.Capped {
		t.Fatalf("report %+v, want file %s, 1 commit, 4 files (tracked.txt, new.txt, image.bin, link)", got, want)
	}
	text := read(t, want)
	for _, has := range []string{"## repo ", "# 1 unpushed commits", "an unpushed change", "with a body", "+third, uncommitted", "+untracked text", "Binary files /dev/null and b/image.bin differ"} {
		if !strings.Contains(text, has) {
			t.Errorf("diff lacks %q:\n%s", has, text)
		}
	}
	for _, not := range []string{"SECRET-IN-AN-IGNORED-FILE", "SECRET-OUTSIDE-THE-TREE", "ignored.bin"} {
		if strings.Contains(text, not) {
			t.Errorf("diff holds %q", not)
		}
	}
	if git("rev-parse", "HEAD") != headBefore || git("status", "--porcelain") != indexBefore {
		t.Error("saving the diff changed the branch or the index")
	}
}

func TestSaveDiffWritesNothingWhenEverythingIsPushed(t *testing.T) {
	root, _, repo, _ := diffTestRepo(t)
	writeFile(t, filepath.Join(root, "repo", "ignored.bin"), "ignored files do not count")
	folder := filepath.Join(t.TempDir(), "archives")
	got := saveDiff(context.Background(), root, folder, "ses_1", "s", []contract.BoxRepo{repo}, runGit)
	if got != (contract.DiffSavedRequest{}) {
		t.Fatalf("report %+v, want an empty one", got)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("the folder was made: %v", err)
	}
}

func TestSaveDiffOverTheCapHoldsTheCommitListAndStatOnly(t *testing.T) {
	root, _, repo, _ := diffTestRepo(t)
	writeFile(t, filepath.Join(root, "repo", "big.txt"), strings.Repeat("BIG-LINE-OF-TEXT\n", (diffCap/17)+10))
	writeFile(t, filepath.Join(root, "repo", "small.txt"), "small")
	got := saveDiff(context.Background(), root, t.TempDir(), "ses_1", "s", []contract.BoxRepo{repo}, runGit)
	if got.Error != "" || !got.Capped || got.Files != 2 {
		t.Fatalf("report %+v, want 2 files and capped", got)
	}
	text := read(t, got.File)
	if !strings.Contains(text, "is over 10 MiB") || !strings.Contains(text, "big.txt") || strings.Contains(text, "BIG-LINE-OF-TEXT") {
		t.Fatalf("a capped section holds the stat and no content, got %d bytes", len(text))
	}
}

func TestSaveDiffGivesEachWorktreeItsOwnSection(t *testing.T) {
	root, _, repo, git := diffTestRepo(t)
	repo.Base = "v2/s" // the pushed branch of this test's origin stands for the default branch
	tree := filepath.Join(t.TempDir(), "worker")
	git("worktree", "add", "-b", "v2/s-worker", tree)
	writeFile(t, filepath.Join(tree, "worker.txt"), "worker's file")
	writeFile(t, filepath.Join(root, "repo", "main.txt"), "main's file")
	got := saveDiff(context.Background(), root, t.TempDir(), "ses_1", "s", []contract.BoxRepo{repo}, runGit)
	text := read(t, got.File)
	if got.Error != "" || strings.Count(text, "## repo ") != 2 || !strings.Contains(text, "## repo "+tree) || !strings.Contains(text, "+worker's file") || !strings.Contains(text, "+main's file") {
		t.Fatalf("report %+v, text:\n%s", got, text)
	}
}

func TestSaveDiffThatCannotWriteReportsItAndLeavesNoFile(t *testing.T) {
	root, _, repo, _ := diffTestRepo(t)
	writeFile(t, filepath.Join(root, "repo", "new.txt"), "work")
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker, "not a folder")
	got := saveDiff(context.Background(), root, filepath.Join(blocker, "archives"), "ses_1", "s", []contract.BoxRepo{repo}, runGit)
	if got.Error == "" || got.File != "" {
		t.Fatalf("report %+v, want an error and no file", got)
	}
}
