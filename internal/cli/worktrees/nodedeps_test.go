package worktrees

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

func TestNewPreparesNodeModules(t *testing.T) {
	storetest.Isolate(t)
	serveWorktreeEngine(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	repo := commandRepo(t)
	writeWorktreeFile(t, filepath.Join(repo, ".gitignore"), "node_modules/\n")
	writeWorktreeFile(t, filepath.Join(repo, "desktop", "frontend", "package.json"), `{"name": "frontend"}`)
	writeWorktreeFile(t, filepath.Join(repo, "desktop", "frontend", "package-lock.json"), `{"lockfileVersion": 3}`)
	commandGit(t, repo, "add", "-A")
	commandGit(t, repo, "commit", "-m", "frontend")
	remote := t.TempDir()
	commandGit(t, remote, "init", "--bare")
	commandGit(t, repo, "remote", "add", "origin", remote)
	commandGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	root := filepath.Join(t.TempDir(), "managed trees")
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(settings.KeyWorktreesRoot, root); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	npmLog := fakeWorktreeNPM(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	newWorktree := func(branch string, extra ...string) (string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		cmd := NewCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(append([]string{"new", branch, "--repo", repo}, extra...))
		if err := cmd.ExecuteContext(ctx); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String()), errOut.String()
	}

	path, errOut := newWorktree("feature/deps")
	nodeModules := filepath.Join(path, "desktop", "frontend", "node_modules")
	mustExistWorktree(t, filepath.Join(nodeModules, ".installed-by-fake-npm"))
	if !strings.Contains(errOut, "desktop/frontend: npm ci in") {
		t.Fatalf("prepare line missing: %q", errOut)
	}
	if got := strings.TrimSpace(readWorktreeFile(t, filepath.Join(nodeModules, ".dyna-lock-sha256"))); got != worktreeLockHash(t, filepath.Join(path, "desktop", "frontend", "package-lock.json")) {
		t.Fatalf("stamp = %q", got)
	}

	// The second worktree copies from the first instead of running npm ci.
	copyPath, copyOut := newWorktree("feature/copied")
	if !strings.Contains(copyOut, "copied node_modules from "+filepath.Clean(path)) {
		t.Fatalf("copy line missing: %q", copyOut)
	}
	mustExistWorktree(t, filepath.Join(copyPath, "desktop", "frontend", "node_modules", ".installed-by-fake-npm"))
	if b := readWorktreeFile(t, npmLog); strings.Count(strings.TrimSpace(b), "\n") != 0 {
		t.Fatalf("npm ran more than once: %q", b)
	}

	skipPath, skipOut := newWorktree("feature/skip", "--no-deps")
	if _, err := os.Stat(filepath.Join(skipPath, "desktop", "frontend", "node_modules")); err == nil {
		t.Fatal("--no-deps restored node_modules")
	}
	if skipOut != "" {
		t.Fatalf("--no-deps logged: %q", skipOut)
	}
}

// fakeWorktreeNPM puts a fake npm first on PATH. It writes its working
// directory to the returned log on every npm ci run.
func fakeWorktreeNPM(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	script := `#!/bin/sh
echo "$PWD" >> "$FAKE_NPM_LOG"
mkdir -p node_modules
echo '{"lockfileVersion": 3}' > node_modules/.package-lock.json
echo installed > node_modules/.installed-by-fake-npm
`
	writeWorktreeFile(t, filepath.Join(bin, "npm"), script)
	if err := os.Chmod(filepath.Join(bin, "npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_NPM_LOG", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func worktreeLockHash(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(readWorktreeFile(t, path)))
	return hex.EncodeToString(sum[:])
}

func readWorktreeFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeWorktreeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustExistWorktree(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
