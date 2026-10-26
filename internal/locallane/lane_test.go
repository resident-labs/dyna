package locallane

import (
	"context"
	"os"
	"os/exec"
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
