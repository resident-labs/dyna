package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A commit reaches a node that has never cached the repository: the push
// creates the node's bare cache first, so a first job on a new node or a new
// repository does not fail before its command runs.
func TestPushCommitCreatesTheNodeCache(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first")
	sha := git("rev-parse", "HEAD")
	cache := filepath.Join(t.TempDir(), "node", "repo.git")
	x := Executor{Node: Conn{Name: "here"}}
	if err := x.PushCommit(t.Context(), repo, cache, sha); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "--git-dir="+cache, "rev-parse", "refs/dyna/ci/"+sha).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != sha {
		t.Fatalf("node cache ref = %q, %v; want %s", out, err, sha)
	}
}

// Two jobs of one commit can race on the node. The second push is refused
// because the first job created the same ref between the second push's
// advertisement and its update. The ref holds the commit the second job
// wants, so the upload succeeds and the job continues.
func TestPushCommitAcceptsRefThatAlreadyHoldsTheCommit(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first")
	sha := git("rev-parse", "HEAD")
	cache := filepath.Join(t.TempDir(), "node", "repo.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", cache).CombinedOutput(); err != nil {
		t.Fatalf("init cache: %v: %s", err, out)
	}
	// The update hook plays the first job: it creates the same ref, then refuses
	// this push, so the push fails while the ref already holds the commit.
	hook := "#!/bin/sh\ngit update-ref refs/dyna/ci/" + sha + " " + sha + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(cache, "hooks", "update"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	x := Executor{Node: Conn{Name: "here"}}
	if err := x.PushCommit(t.Context(), repo, cache, sha); err != nil {
		t.Fatalf("PushCommit = %v; want success when the ref already holds %s", err, sha)
	}
}
