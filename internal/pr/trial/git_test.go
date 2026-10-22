package trial

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type repository struct {
	t           *testing.T
	dir, remote string
}

func (r repository) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("/usr/bin/git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepository(t *testing.T) (repository, Client, Batch) {
	t.Helper()
	r := repository{t, filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "remote.git")}
	if err := os.Mkdir(r.dir, 0700); err != nil {
		t.Fatal(err)
	}
	r.git("init", "--quiet", "--initial-branch=main")
	r.git("config", "user.name", "test")
	r.git("config", "user.email", "test@example.invalid")
	r.git("init", "--quiet", "--bare", r.remote)
	r.git("remote", "add", "origin", r.remote)
	base := r.commit("main", "shared", "base\n")
	a := r.commit("a", "shared", "a\n")
	r.git("checkout", "--quiet", "main")
	bad := r.commit("conflict", "shared", "conflict\n")
	r.git("checkout", "--quiet", "main")
	b := r.commit("b", "other", "b\n")
	r.git("push", "--quiet", "origin", "main", "a", "b", "conflict")
	client := Client{Scratch: t.TempDir(), Run: func(_ context.Context, cmd *exec.Cmd) error { cmd.Path = "/usr/bin/git"; return cmd.Run() }}
	batch := Batch{Remote: r.remote, DefaultBranch: "main", Base: base, Branch: "dyna/merge-trial/1", Heads: []string{a, bad, b}, Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return r, client, batch
}

func (r repository) commit(branch, file, body string) string {
	r.t.Helper()
	if branch != "main" {
		r.git("checkout", "--quiet", "-b", branch)
	}
	if err := os.WriteFile(filepath.Join(r.dir, file), []byte(body), 0600); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", file)
	r.git("commit", "--quiet", "--no-gpg-sign", "-m", branch)
	return r.git("rev-parse", "HEAD")
}

func TestBuildTrialKeepsOrderAndSkipsConflict(t *testing.T) {
	r, client, batch := newRepository(t)
	result, err := client.Build(context.Background(), batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := client.Build(context.Background(), batch, nil); err != nil || replay.Head != result.Head {
		t.Fatalf("repeated build changed its commit: %v", err)
	}
	if strings.Join(result.Included, ",") != strings.Join([]string{batch.Heads[0], batch.Heads[2]}, ",") || strings.Join(result.Conflicts, ",") != batch.Heads[1] {
		t.Fatalf("unexpected result: %+v", result)
	}
	r.git("fetch", "--quiet", "origin", batch.Branch)
	if got := r.git("rev-parse", "FETCH_HEAD"); got != result.Head {
		t.Fatalf("pushed %s, built %s", got, result.Head)
	}
	parents := strings.Fields(r.git("rev-list", "--parents", "-n", "1", result.Head))
	if len(parents) != 3 || parents[2] != batch.Heads[2] {
		t.Fatal("last PR is not a merge commit")
	}
	first := strings.Fields(r.git("rev-list", "--parents", "-n", "1", parents[1]))
	if len(first) != 3 || first[1] != batch.Base || first[2] != batch.Heads[0] {
		t.Fatal("first PR was fast-forwarded or order changed")
	}
	if got := r.git("ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, batch.Base) {
		t.Fatal("building a trial changed main")
	}
	if err := client.Publish(context.Background(), batch, result.Head); err != nil {
		t.Fatal(err)
	}
	if got := r.git("ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, result.Head) {
		t.Fatal("main did not reach the exact tested commit")
	}
	if err := client.Publish(context.Background(), batch, result.Head); err != nil {
		t.Fatal("repeated publication failed:", err)
	}
	if err := client.Delete(context.Background(), batch, result.Head); err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(context.Background(), batch, result.Head); err != nil {
		t.Fatal("cleanup was not idempotent:", err)
	}
	if r.git("ls-remote", "origin", "refs/heads/"+batch.Branch) != "" {
		t.Fatal("trial branch remained")
	}
}

func TestPublishRejectsOutsideMainMovement(t *testing.T) {
	r, client, batch := newRepository(t)
	result, err := client.Build(context.Background(), batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.git("fetch", "--quiet", "origin", batch.Branch)
	// Main moves to an ancestor of the tested result. A plain fast-forward push
	// would succeed, but the trial service must discard this stale base.
	outside := r.git("rev-parse", result.Head+"^1")
	raced := false
	client.Run = func(_ context.Context, cmd *exec.Cmd) error {
		if cmd.Args[1] == "push" && strings.HasSuffix(cmd.Args[len(cmd.Args)-1], ":refs/heads/main") {
			raced = true
			r.git("push", "--quiet", "origin", outside+":refs/heads/main")
		}
		cmd.Path = "/usr/bin/git"
		return cmd.Run()
	}
	if err := client.Publish(context.Background(), batch, result.Head); !errors.Is(err, ErrMainMoved) {
		t.Fatalf("got %v, want main movement", err)
	}
	if got := r.git("ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, outside) {
		t.Fatal("publication overwrote outside movement")
	}
	if !raced {
		t.Fatal("test did not reach the atomic publication guard")
	}
}

func TestPublishRefusesNonFastForwardCommit(t *testing.T) {
	r, client, batch := newRepository(t)
	batch.Base = batch.Heads[0]
	head := batch.Heads[2]
	r.git("push", "--quiet", "origin", batch.Base+":refs/heads/main", head+":refs/heads/"+batch.Branch)
	if err := client.Publish(context.Background(), batch, head); err == nil {
		t.Fatal("published a non-fast-forward commit")
	}
	if got := r.git("ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, batch.Base) {
		t.Fatal("non-fast-forward check changed main")
	}
}

func TestTrialRequiresScopedRunnerAndTrialBranch(t *testing.T) {
	_, client, batch := newRepository(t)
	batch.Branch = "main"
	if _, err := client.Build(context.Background(), batch, nil); err == nil {
		t.Fatal("accepted a non-trial publication branch")
	}
	batch.Branch = "dyna/merge-trial/1"
	client.Run = nil
	if _, err := client.Build(context.Background(), batch, nil); err == nil {
		t.Fatal("ran without a scoped Git transport")
	}
}

func TestBuildDoesNotPushBeforeSavingItsCommit(t *testing.T) {
	r, client, batch := newRepository(t)
	stop := errors.New("cannot save trial state")
	if _, err := client.Build(context.Background(), batch, func(Result) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("got %v", err)
	}
	if r.git("ls-remote", "origin", "refs/heads/"+batch.Branch) != "" {
		t.Fatal("pushed a trial whose commit was not saved")
	}
}

func TestReviewReadsOnlyBaseSourceAndFrozenPatches(t *testing.T) {
	r, client, b := newRepository(t)
	target := filepath.Join(t.TempDir(), "review")
	if err := client.PrepareReview(t.Context(), b, target); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(target, "base", "shared"))
	if err != nil || string(body) != "base\n" {
		t.Fatalf("base=%q error=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(target, "base", ".git")); !os.IsNotExist(err) {
		t.Fatal("reader got Git metadata")
	}
	patch, err := os.ReadFile(filepath.Join(target, "pr-1.patch"))
	if err != nil || !strings.Contains(string(patch), "+a\n") {
		t.Fatalf("patch=%q error=%v", patch, err)
	}
	if main := r.git("ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(main, b.Base) {
		t.Fatal("review input changed main")
	}
	if refs := r.git("ls-remote", "origin", "refs/heads/"+b.Branch); refs != "" {
		t.Fatal("review input pushed a trial")
	}
}

func TestLocalCommonDirRemote(t *testing.T) {
	r, client, batch := newRepository(t)
	batch.Remote = filepath.Join(r.dir, ".git")
	batch.DefaultBranch = "dyna/integration"
	r.git("branch", batch.DefaultBranch, batch.Base)
	result, err := client.Build(context.Background(), batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), batch, result.Head); err != nil {
		t.Fatal(err)
	}
	if got := r.git("rev-parse", batch.DefaultBranch); got != result.Head {
		t.Fatalf("integration = %s", got)
	}
	if err := client.Delete(context.Background(), batch, result.Head); err != nil {
		t.Fatal(err)
	}
	if got := r.git("for-each-ref", "--format=%(refname)", "refs/heads/"+batch.Branch); got != "" {
		t.Fatalf("trial remains: %s", got)
	}
	r.git("update-ref", "refs/heads/"+batch.DefaultBranch, batch.Heads[0])
	if err := client.Publish(context.Background(), batch, result.Head); !errors.Is(err, ErrMainMoved) {
		t.Fatalf("outside move = %v", err)
	}
}

func TestBuildTrialNamesPRInMergeCommit(t *testing.T) {
	r, client, batch := newRepository(t)
	batch.Messages = map[string]string{batch.Heads[0]: MergeMessage(12, "Fix parser\nbounds", batch.Heads[0])}
	result, err := client.Build(context.Background(), batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.git("fetch", "--quiet", "origin", batch.Branch)
	parents := strings.Fields(r.git("rev-list", "--parents", "-n", "1", result.Head))
	if got := r.git("log", "-1", "--format=%B", parents[1]); got != "Merge #12: Fix parser bounds\n\nPR head: "+batch.Heads[0] {
		t.Fatalf("named merge commit message = %q", got)
	}
	if got := r.git("log", "-1", "--format=%s", result.Head); got != "Merge commit '"+batch.Heads[2]+"' into HEAD" {
		t.Fatalf("unnamed merge commit message = %q", got)
	}
}
