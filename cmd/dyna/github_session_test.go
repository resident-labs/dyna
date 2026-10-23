package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
)

func TestReadOnlyGitHubSessionEnvironment(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "work-guard"})
	if err != nil {
		t.Fatal(err)
	}
	rec = roomfixture.Repository(t, rooms, rec.ID, "github.com", "work/guard", "read-only")
	bare := filepath.Join(t.TempDir(), "bare.git")
	for _, args := range [][]string{
		{"init", "--bare", "-q", bare},
		{"-C", rec.Repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgSign=false", "commit", "--allow-empty", "-qm", "initial"},
		{"-C", rec.Repo, "config", "--add", "remote.origin.pushurl", bare},
	} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	bin := t.TempDir()
	if err := os.Symlink("/usr/bin/git", filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	writes := filepath.Join(t.TempDir(), "github-writes")
	t.Setenv("GH_SESSION_WRITES", writes)
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"work"}]}}';;
"auth token") echo work-session-secret;;
"api --hostname") echo '{"permissions":{"push":true}}';;
"pr list") echo 'read allowed';;
*) echo write >> "$GH_SESSION_WRITES"; echo 'unexpected write';;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	private, err := home.Prepare(t.TempDir(), binPath, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	inherited := append([]string{}, os.Environ()...)
	for key, value := range private {
		inherited = append(inherited, key+"="+value)
	}
	session, err := ghclient.SessionEnvironment(context.Background(), rec.ID, t.TempDir(), binPath, inherited)
	if err != nil {
		t.Fatal(err)
	}
	env := append([]string{}, inherited...)
	for key, value := range session.Env {
		env = append(env, key+"="+value)
	}
	env = append(env, mode.EnvRoomID+"="+rec.ID)
	run := func(args ...string) (string, error) {
		cmd := exec.Command("/bin/sh", append([]string{"-c", `exec "$@"`, "session-tool"}, args...)...)
		cmd.Dir, cmd.Env = rec.Repo, env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("gh", "pr", "list"); err != nil || strings.TrimSpace(out) != "read allowed" {
		t.Fatalf("session read: %s %v", out, err)
	}
	for _, args := range [][]string{
		{"gh", "pr", "create"},
		{"gh", "pr", "merge", "1"},
		{"gh", "api", "repos/work/guard/issues", "-X", "POST"},
		{"git", "push", "origin", "HEAD"},
		{"git", "push", "https://github.com/work/guard.git", "HEAD"},
	} {
		out, err := run(args...)
		if err == nil || !strings.Contains(out, "read-only") {
			t.Fatalf("session write guard: %v: %s %v", args, out, err)
		}
	}
	if _, err := os.Stat(writes); !os.IsNotExist(err) {
		t.Fatal("read-only session reached a GitHub write command")
	}
	if out, err := exec.Command("/usr/bin/git", "--git-dir", bare, "show-ref").CombinedOutput(); err == nil || len(out) != 0 {
		t.Fatal("read-only session pushed to the local test remote")
	}
	if out, err := run("git", "remote", "get-url", "origin"); err != nil || strings.TrimSpace(out) != "https://github.com/work/guard.git" {
		t.Fatalf("fetch URL changed: %s %v", out, err)
	}
}
