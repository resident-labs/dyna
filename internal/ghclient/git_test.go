package ghclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/room"
)

func trialGitAccount(t *testing.T) (context.Context, string, string) {
	t.Helper()
	records := setupRooms(t, "work/trial")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh with spaces")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo trial-secret;;
"api --hostname") echo '{"permissions":{"push":true}}';;
"auth git-credential") cat >/dev/null; printf 'username=writer\npassword=%s\n' "$GH_TOKEN";;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithRepository(WithRoom(context.Background(), records[0].ID), "github.com", "work/trial")
	return ctx, gh, records[0].Repo
}

func TestGitUsesRoomAccountAndRedactsErrors(t *testing.T) {
	ctx, gh, _ := trialGitAccount(t)
	t.Setenv("GH_TOKEN", "wrong-global-token")
	cmd := Command(ctx, "/bin/sh", "", "-c", `test "$GH_TOKEN" = trial-secret || exit 2; printf '%s' "$GH_TOKEN" >&2; exit 1`)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := RunGit(ctx, gh, cmd); err == nil {
		t.Fatal("expected command failure")
	}
	if stderr.String() != "[redacted]" {
		t.Fatalf("error was not redacted: %q", stderr.String())
	}
	joined := strings.Join(cmd.Env, "\n")
	if strings.Contains(joined, "wrong-global-token") {
		t.Fatal("inherited token remained")
	}
	if !strings.Contains(joined, "auth git-credential") || !strings.Contains(joined, "credential.https://github.com/work/trial.helper") {
		t.Fatal("Git credentials were not scoped to the repository")
	}
	for _, repo := range []string{"work/trial", "work/trial.git", "work/other"} {
		cmd := Command(ctx, "/usr/bin/git", "", "credential", "fill")
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=" + repo + "\n\n")
		var output strings.Builder
		cmd.Stdout = &output
		err := RunGit(ctx, gh, cmd)
		if repo == "work/other" {
			if err == nil || strings.Contains(output.String(), "trial-secret") {
				t.Fatal("credential helper answered for another repository")
			}
		} else if err != nil || !strings.Contains(output.String(), "password=trial-secret") {
			t.Fatalf("scoped credential helper failed for %s: %v", repo, err)
		}
	}
}

func TestGitRefusesReadOnlyRoomBeforeExecution(t *testing.T) {
	records := setupRooms(t, "work/readonly-trial")
	ctx := WithRepository(WithRoom(context.Background(), records[0].ID), "github.com", "work/readonly-trial")
	marker := filepath.Join(t.TempDir(), "executed")
	cmd := Command(ctx, "/bin/sh", "", "-c", `touch "$1"`, "sh", marker)
	if err := RunGit(ctx, "/missing/gh", cmd); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("read-only command executed")
	}
}

func TestGitDropsInheritedHTTPAuthenticationHeaders(t *testing.T) {
	ctx, gh, dir := trialGitAccount(t)
	var mu sync.Mutex
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, strings.Join(r.Header.Values("Authorization"), ","))
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer server.Close()
	for _, key := range []string{"http.extraHeader", "http.https://github.com/.extraHeader", "http." + server.URL + "/.extraHeader"} {
		if out, err := exec.Command("/usr/bin/git", "-C", dir, "config", "--local", key, "Authorization: Basic wrong-account").CombinedOutput(); err != nil {
			t.Fatalf("config: %v: %s", err, out)
		}
	}
	cmd := Command(ctx, "/usr/bin/git", dir, "ls-remote", server.URL+"/work/trial.git")
	if err := RunGit(ctx, gh, cmd); err == nil {
		t.Fatal("expected missing remote")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) == 0 {
		t.Fatal("Git did not contact the test remote")
	}
	for _, header := range headers {
		if header != "" {
			t.Fatal("Git sent an inherited Authorization header")
		}
	}
	// The source checkout keeps its configured header; overrides belong to the child.
	if out, err := exec.Command("/usr/bin/git", "-C", dir, "config", "--get", "http.extraHeader").Output(); err != nil || !strings.Contains(string(out), "wrong-account") {
		t.Fatal("changed the checkout's Git configuration")
	}
}

// A fetch updates local refs but does not write to the hosted repository.
func TestReadOnlyGitFetchAndPush(t *testing.T) {
	ctx, gh, dir := trialGitAccount(t)
	records, err := room.DefaultStore().List(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadOnly); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	for _, args := range [][]string{{"init", "--initial-branch=main", seed}, {"-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base"}, {"-C", seed, "push", remote, "main:main"}, {"-C", remote, "symbolic-ref", "HEAD", "refs/heads/main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	fetch := Command(ctx, "/usr/bin/git", dir, "-c", "core.hooksPath=/dev/null", "fetch", "--prune", remote)
	var stderr strings.Builder
	fetch.Stderr = &stderr
	if err := RunGit(ctx, gh, fetch); err != nil {
		t.Fatalf("read-only fetch: %v %s", err, stderr.String())
	}
	push := Command(ctx, "/usr/bin/git", dir, "push", remote, "HEAD:main")
	if err := RunGit(ctx, gh, push); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("push: %v", err)
	}
}
