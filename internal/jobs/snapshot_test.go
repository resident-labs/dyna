package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

func TestSnapshotTreePreservesIndexAndIncludesUntracked(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.email", "test@example.invalid")
	gitTest(t, root, "config", "user.name", "Test")
	writeWorkspaceTestFile(t, filepath.Join(root, "tracked"), "first")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "initial")
	writeWorkspaceTestFile(t, filepath.Join(root, "tracked"), "staged")
	gitTest(t, root, "add", "tracked")
	writeWorkspaceTestFile(t, filepath.Join(root, "tracked"), "working")
	writeWorkspaceTestFile(t, filepath.Join(root, "sub", "new"), "new content")
	before := gitTest(t, root, "status", "--porcelain=v1")
	index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	gotRoot, sha, sub, err := SnapshotTree(t.Context(), filepath.Join(root, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root || sub != "sub" {
		t.Fatalf("root/sub = %q/%q", gotRoot, sub)
	}
	if got := gitTest(t, root, "show", sha+":sub/new"); got != "new content" {
		t.Fatal(got)
	}
	if got := gitTest(t, root, "show", sha+":tracked"); got != "working" {
		t.Fatal(got)
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(index) != string(after) || before != gitTest(t, root, "status", "--porcelain=v1") {
		t.Fatal("snapshot changed index or status")
	}
	_, again, _, err := SnapshotTree(t.Context(), filepath.Join(root, "sub"))
	if err != nil || sha != again {
		t.Fatalf("repeat = %s, %v; want %s", again, err, sha)
	}
}

func TestJobScriptRunsInSubdirectory(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	// A source-less script creates its work directory; this exercises its cwd
	// without a remote node or a systemd unit.
	job := Job{Dir: root, Workspace: work, Subdir: "sub", Command: []string{"pwd"}, Timeout: time.Second}
	script := jobScript(machines.Machine{Name: "local"}, job, Resources{}, "")
	cmd := exec.Command("bash", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != filepath.Join(work, "sub") {
		t.Fatalf("cwd = %s", out)
	}
}
