package hooks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fetchRepo builds a Git repository with the given origin URL. No commit is
// created: every test's fetch fails before it could read one, and a commit
// would need an identity CI runners do not have.
func fetchRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "--initial-branch=main", dir},
		{"-C", dir, "remote", "add", "origin", origin},
	} {
		out, err := exec.Command("/usr/bin/git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args, err, out)
		}
	}
	return dir
}

func fetchGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", args, err, out)
	}
}

// A GitHub origin runs through the repository-scoped path: the fetch command
// rewrites the SSH URL to HTTPS — the transport the selected account's
// credential answers — and asks for exactly the default branch refspec.
func TestFetchOriginUsesTheScopedPathForGitHub(t *testing.T) {
	dir := fetchRepo(t, "git@github.com:owner/repo.git")

	var mu sync.Mutex
	var got []string
	scoped, plain := runScopedFetch, plainGitFetch
	runScopedFetch = func(ctx context.Context, ghBinary string, cmd *exec.Cmd) error {
		if ghBinary != "" {
			t.Errorf("scoped fetch passed gh %q, want the resolved default", ghBinary)
		}
		mu.Lock()
		got = cmd.Args
		mu.Unlock()
		return nil
	}
	plainGitFetch = func(context.Context, string, ...string) (string, error) {
		t.Error("a successful scoped fetch must not run the plain fallback")
		return "", nil
	}
	t.Cleanup(func() { runScopedFetch, plainGitFetch = scoped, plain })

	if err := fetchOrigin(context.Background(), dir, "main"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"-c\nurl.https://github.com/owner/repo.git.insteadOf=git@github.com:owner/repo.git",
		"fetch",
		"+refs/heads/main:refs/remotes/origin/main",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scoped fetch lacks %q:\n%s", want, joined)
		}
	}
}

// A scoped path that cannot prove an account falls back to plain Git: a plain
// success is a success (a public repository outside every room keeps updating),
// and only a plain fetch that also fails reports, naming both errors.
func TestFetchOriginFallsBackToPlainGit(t *testing.T) {
	dir := fetchRepo(t, "https://github.com/owner/repo.git")

	scoped, plain := runScopedFetch, plainGitFetch
	runScopedFetch = func(context.Context, string, *exec.Cmd) error {
		return errors.New("keyring locked")
	}
	var plainCalls int
	plainGitFetch = func(context.Context, string, ...string) (string, error) {
		plainCalls++
		if plainCalls == 1 {
			return "", nil
		}
		return "", errors.New("could not read Username")
	}
	t.Cleanup(func() { runScopedFetch, plainGitFetch = scoped, plain })

	if err := fetchOrigin(context.Background(), dir, "main"); err != nil {
		t.Fatalf("a plain success must swallow the scoped error: %v", err)
	}
	err := fetchOrigin(context.Background(), dir, "main")
	if err == nil || !strings.Contains(err.Error(), "keyring locked") || !strings.Contains(err.Error(), "could not read Username") {
		t.Fatalf("both failures must report, got: %v", err)
	}
}

// An origin that is not GitHub keeps its configured credential helpers: the
// plain fetch consults them, so an existing helper still answers. The server
// refuses every request, which makes the fetch fail while proving the helper
// ran.
func TestFetchOriginKeepsConfiguredHelpersForOtherHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	dir := fetchRepo(t, srv.URL+"/repo.git")
	marker := filepath.Join(t.TempDir(), "helper-ran")
	helper := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ncat >/dev/null\necho username=owner\necho password=secret\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fetchGit(t, "-C", dir, "config", "credential.helper", "!"+helper)

	scoped := runScopedFetch
	runScopedFetch = func(context.Context, string, *exec.Cmd) error {
		t.Error("a non-GitHub origin must not take the scoped path")
		return nil
	}
	t.Cleanup(func() { runScopedFetch = scoped })

	if err := fetchOrigin(context.Background(), dir, "main"); err == nil {
		t.Fatal("a fetch against a refusing server succeeded")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the configured credential helper was not consulted: %v", err)
	}
}

// The scoped path is the real ghclient.RunGit with the repository scope set:
// with no room matching the repository, RunGit refuses on rooms, not on the
// missing repository scope, and the plain fallback reports alongside it.
func TestFetchOriginReachesRunGitWithTheRepositoryScope(t *testing.T) {
	dir := fetchRepo(t, "https://github.com/owner/repo.git")

	plain := plainGitFetch
	plainGitFetch = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("plain marker")
	}
	t.Cleanup(func() { plainGitFetch = plain })

	err := fetchOrigin(context.Background(), dir, "main")
	if err == nil || !strings.Contains(err.Error(), "plain marker") {
		t.Fatalf("the plain fallback must report: %v", err)
	}
	if strings.Contains(err.Error(), "explicit GitHub repository scope") {
		t.Fatalf("the scoped fetch ran without its repository scope: %v", err)
	}
}
