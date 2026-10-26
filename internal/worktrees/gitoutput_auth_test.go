package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
	"dyna-agent/internal/testsafe"
)

// The engine has no turn-specific Git helper, and the desktop keyring may
// be unavailable. Managed fetches must use the room account, like gh does.
func TestManagedFetchUsesRoomAccountWithoutKeyring(t *testing.T) {
	repo := gcRepo(t)
	git(t, repo, "remote", "set-url", "origin", "https://github.com/Work/Managed.git")
	git(t, repo, "config", "credential.helper", "")
	git(t, repo, "config", "remote.origin.vcs", "managed-test")
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "managed-fetch", Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	gh := filepath.Join(bin, "gh")
	if err := os.WriteFile(gh, []byte(`#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") exit 1;;
"api --hostname") case "$4" in
user) echo '{"login":"worker"}';;
repos/work/managed|repos/Work/Managed) echo '{"permissions":{"push":true}}';;
*) exit 1;;
esac;;
"auth git-credential") cat >/dev/null; printf 'username=worker\npassword=%s\n' "$GH_TOKEN";;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	// A fake remote transport checks real Git credential resolution, then
	// advertises a commit already present in the fixture. No network runs.
	helper := filepath.Join(bin, "git-remote-managed-test")
	if err := os.WriteFile(helper, []byte(`#!/bin/sh
credentials=$(printf 'protocol=https\nhost=github.com\npath=Work/Managed.git\n\n' | /usr/bin/git credential fill) || exit 1
printf '%s\n' "$credentials" | /usr/bin/grep -q '^password=session-secret$' || exit 1
while read -r command; do
case "$command" in
capabilities) printf 'fetch\n\n';;
list*) printf '%s refs/heads/main\n\n' "$MANAGED_HEAD";;
esac
done
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(ghclient.BinaryEnv, gh)
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "session-secret")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	testsafe.DisableGitAutoMaintenance()
	t.Setenv("MANAGED_HEAD", git(t, repo, "rev-parse", "HEAD"))
	ctx := ghclient.WithRoom(context.Background(), rec.ID)
	tree, err := NewFor(ctx, repo, "authenticated-fetch", "origin/main", "", "")
	if err != nil {
		t.Fatalf("managed worktree fetch: %v", err)
	}
	if reason, err := refreshDefaultBranch(ctx, repo); err != nil || reason != "" {
		t.Fatalf("cleanup fetch: %s: %v", reason, err)
	}
	out, err := gitOriginOutput(ctx, repo, "ls-remote", "--exit-code", "origin", "refs/heads/main")
	if err != nil || !strings.Contains(string(out), "refs/heads/main") {
		t.Fatalf("cleanup remote check: %v: %s", err, out)
	}
	if tree.Branch != "authenticated-fetch" {
		t.Fatalf("unexpected worktree: %+v", tree)
	}
}
