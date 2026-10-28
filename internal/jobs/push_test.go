package jobs

import (
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
