package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The build name comes from the committed third_party/pi tree, but the tarball
// packs the working files: uncommitted Pi changes would ship under a stale
// build name and collide with the real build of that tree. The pack refuses
// them instead.
func TestPiPackRefusesUncommittedPiChanges(t *testing.T) {
	script, err := filepath.Abs("pi-pack.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) []byte {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	piDir := filepath.Join(repo, "third_party", "pi")
	if err := os.MkdirAll(piDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(piDir, "package.json"), []byte("{}\\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "pi")
	if err := os.WriteFile(filepath.Join(piDir, "local.ts"), []byte("dirty\\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, filepath.Join(repo, "out.tgz"))
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("pi-pack packed uncommitted changes: %s", out)
	}
	if !strings.Contains(string(out), "uncommitted") {
		t.Fatalf("pi-pack gave no clear reason: %s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "out.tgz")); err == nil {
		t.Fatal("pi-pack wrote a tarball despite refusing")
	}
}
