package locallane

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/settings"
)

func TestEnabledReadsCommonDirsOncePerContext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()
	if out, err := exec.Command("/usr/bin/git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	v.LocalLaneRepos = []string{repo}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	ctx := WithCommonDirs(context.Background())
	if enabled, err := Enabled(ctx, repo); err != nil || !enabled {
		t.Fatalf("first check = %v, %v", enabled, err)
	}
	// Git fails without the repository, so a cached check proves it ran no git.
	if err := os.Rename(repo, repo+".away"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(repo+".away", repo) })
	if enabled, err := Enabled(ctx, repo); err != nil || !enabled {
		t.Fatalf("second check in one tick ran git: %v, %v", enabled, err)
	}
	if _, err := Enabled(context.Background(), repo); err == nil {
		t.Fatal("a context without the cache must read Git again")
	}
}

// D-286: nothing else can land an approved change in a repository with no
// remote, so it uses the lane without a setting. A repository with a remote
// keeps the setting as its only switch.
func TestRepositoryWithNoRemoteUsesTheLane(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	local, hosted := t.TempDir(), t.TempDir()
	for _, repo := range []string{local, hosted} {
		if out, err := exec.Command("/usr/bin/git", "init", "-q", repo).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	if out, err := exec.Command("/usr/bin/git", "-C", hosted, "remote", "add", "origin", "https://github.com/o/r.git").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if enabled, err := Enabled(context.Background(), local); err != nil || !enabled {
		t.Fatalf("repository with no remote = %v, %v; want the lane", enabled, err)
	}
	if enabled, err := Enabled(context.Background(), hosted); err != nil || enabled {
		t.Fatalf("repository with a remote and no setting = %v, %v; want no lane", enabled, err)
	}
}

// A change opened from a feature worktree of a repository with no remote must
// not start integration at the feature head: the diff would be empty and the
// commits would land before review.
func TestEnsureStartsIntegrationAtTheBaseBranchNotTheFeatureWorktree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	run(repo, "init", "-q", "-b", "main")
	run(repo, "commit", "-q", "--allow-empty", "-m", "base")
	base := run(repo, "rev-parse", "HEAD")
	feature := filepath.Join(t.TempDir(), "feature")
	run(repo, "worktree", "add", "-q", "-b", "feature", feature)
	run(feature, "commit", "-q", "--allow-empty", "-m", "the change")
	got, err := Ensure(context.Background(), feature)
	if err != nil || got != base {
		t.Fatalf("integration starts at %q, %v; want the base branch head %q", got, err, base)
	}
}
