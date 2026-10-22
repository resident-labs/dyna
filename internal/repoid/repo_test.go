package repoid

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRemoteTransportIdentity(t *testing.T) {
	for _, remote := range []string{"git@github.com:Example/Project.git", "https://github.com/example/project.git", "ssh://git@github.com/EXAMPLE/PROJECT.git"} {
		got, err := RemoteIdentity(t.TempDir(), remote)
		if err != nil || got != "github.com/example/project" {
			t.Fatalf("%s => %s, %v", remote, got, err)
		}
	}
}
func TestResolveWithoutRemoteUsesRootCommit(t *testing.T) {
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			t.Cleanup(func() { _ = os.Setenv(key, value) })
			_ = os.Unsetenv(key)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := Git(ctx, repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init", "-b", "main")
	commit := func() {
		git("-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "test")
	}
	commit()
	root := git("rev-parse", "HEAD")
	commit()
	r, err := Resolve(ctx, repo)
	if err != nil || r.ID != root || r.Main != repo {
		t.Fatalf("fallback: %#v %v", r, err)
	}
	common := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	fromCommon, err := Resolve(ctx, common)
	if err != nil || fromCommon != r {
		t.Fatalf("common Git directory: %#v %v, want %#v", fromCommon, err, r)
	}
}
