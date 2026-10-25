package worktrees

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func gcRepo(t *testing.T) string {
	t.Helper()
	storetest.Isolate(t)
	v := settings.Defaults()
	if err := v.Set(settings.KeyWorktreesRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	repo := copyFixture(t, fixtures.gc)
	remote := copyFixture(t, fixtures.gcRemote)
	git(t, repo, "remote", "set-url", "origin", remote)
	return repo
}

func gcTree(t *testing.T, repo, branch, owner string) string {
	t.Helper()
	return gcTreeAt(t, repo, branch, "origin/main", owner)
}

func gcTreeAt(t *testing.T, repo, branch, base, owner string) string {
	t.Helper()
	rec, err := NewFor(context.Background(), repo, branch, base, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	return rec.Path
}

func useTrunk(t *testing.T, repo string) {
	t.Helper()
	remote := git(t, repo, "remote", "get-url", "origin")
	git(t, repo, "branch", "-M", "trunk")
	git(t, remote, "branch", "-m", "main", "trunk")
	git(t, remote, "symbolic-ref", "HEAD", "refs/heads/trunk")
	git(t, repo, "update-ref", "refs/remotes/origin/trunk", "trunk")
	git(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
}

func ageGCTree(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-25 * time.Hour)
	for _, root := range []string{path, git(t, path, "rev-parse", "--absolute-git-dir")} {
		if err := filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Chtimes(p, old, old)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGCWithoutLinkedWorktreesNeedsNoRemote(t *testing.T) {
	repo := gcRepo(t)
	git(t, repo, "remote", "remove", "origin")
	trees, err := GC(context.Background(), repo, GCOptions{})
	if err != nil {
		t.Fatalf("cleanup of a local main checkout requires no remote: %v", err)
	}
	if len(trees) != 1 || !trees[0].Main || trees[0].Action != "kept" || trees[0].Reason != "main checkout" {
		t.Fatalf("main checkout must remain protected: %#v", trees)
	}
}

func TestGCContinuesAfterRemovingItsStartingWorktree(t *testing.T) {
	repo := gcRepo(t)
	useTrunk(t, repo)
	first := gcTreeAt(t, repo, "cleanup-start", "origin/trunk", "closed-owner")
	second := gcTreeAt(t, repo, "cleanup-next", "origin/trunk", "closed-owner")
	trees, err := GC(context.Background(), first, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatalf("cleanup must use a repository directory that survives removal: %v", err)
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("closed owner worktree remains: %s: %v; decisions: %#v", path, err, trees)
		}
	}
}

func TestGCKeepsUnpublishedWorktreeWithoutOriginHead(t *testing.T) {
	repo := gcRepo(t)
	useTrunk(t, repo)
	git(t, repo, "config", "remote.origin.followRemoteHEAD", "never")
	git(t, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	path := gcTreeAt(t, repo, "unpublished", "origin/trunk", "closed-owner")
	git(t, path, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "unpublished")
	git(t, repo, "merge", "--ff-only", "unpublished")
	opts := GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }}
	trees, err := GC(context.Background(), repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tree := range trees {
		if tree.Path == path {
			found = tree.Action == "kept" && !tree.Merged && tree.Ahead == 1 && tree.Reason == ReasonNotMerged
		}
	}
	if !found {
		t.Fatalf("cleanup did not preserve unpublished work: %+v", trees)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unpublished worktree was removed: %v", err)
	}

	git(t, repo, "push", "origin", "trunk")
	if _, err := GC(context.Background(), repo, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("published worktree was not removed after the ancestry recheck: %v", err)
	}
}

func TestGCKeepsLiveProcessWorktree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /proc working directories")
	}
	for _, subdir := range []string{".", "nested"} {
		t.Run(subdir, func(t *testing.T) {
			repo := gcRepo(t)
			path := filepath.Join(t.TempDir(), "handmade")
			git(t, repo, "worktree", "add", "-b", "live", path, "origin/main")
			cwd := filepath.Join(path, subdir)
			if err := os.MkdirAll(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			ageGCTree(t, path)
			child := exec.Command("sleep", "60")
			child.Dir = cwd
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child.ProcessState == nil {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			})
			options := GCOptions{Path: path}
			actions, err := GC(context.Background(), repo, options)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("in use by process %d (sleep)", child.Process.Pid)
			if len(actions) != 1 || actions[0].Action != "kept" || actions[0].Reason != want {
				t.Fatalf("live process: %#v, want kept with %q", actions, want)
			}
			if _, err := os.Stat(cwd); err != nil {
				t.Fatal("live process working directory removed:", err)
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			actions, err = GC(context.Background(), repo, options)
			if err != nil {
				t.Fatal(err)
			}
			if len(actions) != 1 || actions[0].Action != "removed" {
				t.Fatalf("exited process: %#v", actions)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("unused tree still exists: %v", err)
			}
		})
	}
}

// The safety matrix runs in two halves so each stays fast: who owns a tree,
// and what the tree holds.
// Each safety test builds at most three trees: each tree is a real git
// worktree, and one test must stay under a second on a busy CI runner.
func TestGCSafetyKnownOwner(t *testing.T) {
	gcSafetyMatrix(t, "closed", "open")
}

func TestGCSafetyUnknownOwner(t *testing.T) {
	gcSafetyMatrix(t, "unknown-recent", "unknown-old")
}

func TestGCSafetyChangedContents(t *testing.T) {
	gcSafetyMatrix(t, "dirty", "untracked", "unmerged")
}

func TestGCSafetyLockedAndIgnored(t *testing.T) {
	gcSafetyMatrix(t, "locked", "ignored")
}

// gcSafetyMatrix builds one tree per case on a branch of the same name. Only
// closed, unknown-old and ignored trees may be removed.
func gcSafetyMatrix(t *testing.T, branches ...string) {
	t.Helper()
	repo := gcRepo(t)
	paths := map[string]string{}
	for _, b := range branches {
		owner := "closed"
		if b == "open" {
			owner = "open"
		}
		if strings.HasPrefix(b, "unknown") {
			owner = ""
		}
		paths[b] = gcTree(t, repo, b, owner)
	}
	if p, ok := paths["dirty"]; ok {
		os.WriteFile(filepath.Join(p, "base.txt"), []byte("modified"), 0600)
	}
	if p, ok := paths["untracked"]; ok {
		os.WriteFile(filepath.Join(p, "new.txt"), []byte("untracked"), 0600)
	}
	if p, ok := paths["unmerged"]; ok {
		os.WriteFile(filepath.Join(p, "commit.txt"), []byte("commit"), 0600)
		git(t, p, "add", ".")
		git(t, p, "commit", "-m", "unmerged")
	}
	if p, ok := paths["locked"]; ok {
		git(t, repo, "worktree", "lock", p)
	}
	if p, ok := paths["ignored"]; ok {
		// Ignore a generated file through repository-local excludes.
		os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("generated.bin\n"), 0600)
		os.WriteFile(filepath.Join(p, "generated.bin"), []byte("cache"), 0600)
	}
	if p, ok := paths["unknown-old"]; ok {
		ageGCTree(t, p)
	}
	// One real pass decides every case; TestGCDryRunRemovesNothing covers dry runs.
	actions, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(id string) (bool, error) { return id == "closed", nil }})
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]Tree{}
	for _, a := range actions {
		decisions[a.Branch] = a
	}
	for _, b := range branches {
		want := "kept"
		if b == "closed" || b == "unknown-old" || b == "ignored" {
			want = "removed"
		}
		if got := decisions[b]; got.Action != want {
			t.Errorf("%s: %#v, want %s", b, got, want)
		}
		if _, err := os.Stat(paths[b]); (want == "removed") != os.IsNotExist(err) {
			t.Errorf("%s: stat after GC = %v, want %s", b, err, want)
		}
	}
	for _, b := range branches {
		git(t, repo, "show-ref", "--verify", "refs/heads/"+b)
	}
}

func TestGCIncludesHandmadeAndFailsClosedOnFetch(t *testing.T) {
	repo := gcRepo(t)
	path := filepath.Join(t.TempDir(), "handmade")
	git(t, repo, "worktree", "add", "-b", "handmade", path, "origin/main")
	actions, err := GC(context.Background(), repo, GCOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range actions {
		if a.Path == path {
			found = true
			if a.Action != "kept" || !strings.Contains(a.Reason, "24 hours") {
				t.Fatalf("recent unknown: %#v", a)
			}
		}
	}
	if !found {
		t.Fatal("handmade worktree absent")
	}
	git(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))
	if _, err := GC(context.Background(), repo, GCOptions{}); err == nil {
		t.Fatal("cleanup accepted failed fetch")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsNestedRootAndRecordsOwner(t *testing.T) {
	repo := gcRepo(t)
	rec, err := NewFor(context.Background(), repo, "feature/a", "origin/main", "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := Get(rec.Path)
	if err != nil || stored.Session != "owner" || stored.Branch != "feature/a" || stored.CreatedAt.IsZero() {
		t.Fatalf("record: %#v, %v", stored, err)
	}
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	v.Set(settings.KeyWorktreesRoot, filepath.Join(repo, "nested"))
	v.Save()
	if _, err := NewFor(context.Background(), repo, "nested", "origin/main", "owner", ""); err == nil {
		t.Fatal("accepted root in main checkout")
	}
}

func TestWorkflowUsesSharedRootAndSafeCleanup(t *testing.T) {
	repo := gcRepo(t)
	t.Setenv("DYNA_SESSION", "open")
	path, _, cleanup, err := Create(context.Background(), repo, settings.WorktreesRetentionNone)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := ManagedRoot()
	if !within(root, path) {
		t.Fatalf("workflow outside managed root: %s", path)
	}
	rec, err := Get(path)
	if err != nil || rec.Session != "open" {
		t.Fatalf("owner: %#v %v", rec, err)
	}
	if !cleanup() {
		t.Fatal("cleanup removed active session worktree")
	}
}

func TestChangingRootKeepsOldRegistryUsable(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "old-root", "closed")
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(settings.KeyWorktreesRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(path); err != nil {
		t.Fatalf("old root record lost: %v", err)
	}
	if err := Track(Record{Path: path, Repo: repo, Head: git(t, path, "rev-parse", "HEAD")}); err != nil {
		t.Fatal(err)
	}
	actions, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range actions {
		if a.Path == path && a.Action == "removed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("old root not collected: %#v", actions)
	}
	if _, err := Get(path); err == nil {
		t.Fatal("removed tree still registered")
	}
}

func TestGCRequiresRemoteMainToExist(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "safe", "closed")
	remote := git(t, repo, "remote", "get-url", "origin")
	// Deletion must not depend on the host default branch of a bare repo.
	git(t, remote, "symbolic-ref", "HEAD", "refs/heads/unused")
	git(t, repo, "push", "origin", ":refs/heads/main")
	// The stale origin/main is never trusted: every tree is kept. A remote
	// without main is not a cleanup failure.
	trees, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatalf("cleanup failed for a remote without main: %v", err)
	}
	for _, tree := range trees {
		if tree.Path == path && (tree.Action != "kept" || tree.Reason != "remote has no main branch") {
			t.Fatalf("cleanup trusted stale origin/main after remote branch deletion: %#v", tree)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("worktree removed after failed main fetch")
	}
}

func TestGCProtectsRunningWorkflow(t *testing.T) {
	repo := gcRepo(t)
	path, _, _, err := CreateForRun(context.Background(), repo, settings.WorktreesRetentionNone, "wf_active", "")
	if err != nil {
		t.Fatal(err)
	}
	runstoretest.Write(t, store.DataDir(), runstore.Meta{ID: "wf_active", Status: "running"})
	old := time.Now().Add(-25 * time.Hour)
	for _, p := range []string{path, git(t, path, "rev-parse", "--absolute-git-dir")} {
		if err := filepath.Walk(p, func(p string, i os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			return os.Chtimes(p, old, old)
		}); err != nil {
			t.Fatal(err)
		}
	}
	actions, err := GC(context.Background(), repo, GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.Path == path && (a.Action != "kept" || !a.OwnerOpen) {
			t.Fatalf("active workflow: %#v", a)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active workflow removed")
	}
	runstoretest.Write(t, store.DataDir(), runstore.Meta{ID: "wf_active", Status: "ok", EndedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO run_directories(run_id,body,complete) VALUES('wf_active','[]',1)"); err != nil {
		t.Fatal(err)
	}

	actions, err = GC(context.Background(), repo, GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("closed workflow kept: %#v %v", actions, err)
	}
}

func TestGCChecksUntrackedDespiteGitConfiguration(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "untracked-config", "closed")
	git(t, path, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(path, "important"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	actions, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.Path == path && (!a.Dirty || a.Action != "kept") {
			t.Fatalf("untracked hidden: %#v", a)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("untracked file deleted")
	}
}

func TestGetRejectsAmbiguousBranchBasenames(t *testing.T) {
	repo := gcRepo(t)
	first := gcTree(t, repo, "same-branch", "")
	repo2 := t.TempDir()
	git(t, repo2, "clone", repo, ".")
	second := gcTree(t, repo2, "same-branch", "")
	if filepath.Base(first) != filepath.Base(second) {
		t.Fatal("test requires matching basenames")
	}
	if _, err := Get(filepath.Base(first)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous lookup: %v", err)
	}
	if rec, err := Get(second); err != nil || rec.Path != second {
		t.Fatalf("exact lookup: %#v %v", rec, err)
	}
}

func TestGCInfersAllSessionOwnersBeforeRemoval(t *testing.T) {
	repo := gcRepo(t)
	path := filepath.Join(t.TempDir(), "handmade")
	git(t, repo, "worktree", "add", "-b", "inferred", path, "origin/main")
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	writeSession := func(id, dir, closed string) {
		t.Helper()
		body := map[string]any{"id": id, "name": id, "room": "test", "dir": dir}
		if closed != "" {
			body["closed"] = closed
		}
		recordtest.Write(t, runstore.SessionsDir(), id, body)
	}
	writeSession("ses_closed", path, "2026-01-01T00:00:00Z")
	writeSession("ses_open", nested, "")
	ageGCTree(t, path)
	actions, err := GC(context.Background(), repo, GCOptions{Session: "ses_closed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected one inferred worktree: %#v", actions)
	}
	for _, a := range actions {
		if a.Path == path {
			if a.Action != "kept" || !strings.Contains(a.Reason, "inferred owner is still open") || !strings.Contains(a.Reason, "owner inferred from session test:ses_open") {
				t.Fatalf("inferred active owner: %#v", a)
			}
		}
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatal("open session working directory removed:", err)
	}
	writeSession("ses_open", nested, "2026-01-01T00:00:00Z")
	actions, err = GC(context.Background(), repo, GCOptions{Session: "ses_closed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Action != "removed" {
		t.Fatalf("closed inferred owners: %#v", actions)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fresh tree with closed owners kept: %v", err)
	}
}

func TestGCMarksOnlyTheMainCheckout(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "safe", "closed")
	actions, err := GC(context.Background(), repo, GCOptions{DryRun: true, OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	mains := 0
	for _, a := range actions {
		if a.Main {
			mains++
			if a.Action == "removed" || a.Action == "would remove" {
				t.Fatalf("main checkout marked for removal: %+v", a)
			}
		}
		if a.Path == path && a.Main {
			t.Fatal("linked worktree marked as the main checkout")
		}
	}
	if mains != 1 {
		t.Fatalf("main checkouts = %d, want 1", mains)
	}
}

func TestNewLocalLaneDefaultsToIntegrationAndExplicitBaseWins(t *testing.T) {
	repo := gcRepo(t)
	useTrunk(t, repo)
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	v.LocalLaneRepos = []string{repo}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	first, err := NewFor(context.Background(), repo, "local-first", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Base != "dyna/integration" {
		t.Fatalf("default base = %s", first.Base)
	}
	// Move integration, then prove an explicit base still uses origin/trunk.
	git(t, first.Path, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "local integration")
	head := git(t, first.Path, "rev-parse", "HEAD")
	git(t, repo, "update-ref", "refs/heads/dyna/integration", head)
	second, err := NewFor(context.Background(), first.Path, "local-second", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, second.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("local worktree head = %s", got)
	}
	explicit, err := NewFor(context.Background(), repo, "explicit-trunk", "origin/trunk", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, explicit.Path, "rev-parse", "HEAD"); got == head {
		t.Fatal("explicit base ignored")
	}
}

// An outside clone whose remote has no main branch keeps its trees, and that
// is not a cleanup failure. An unreachable remote still fails closed.
func TestGCKeepsTreesOfARemoteWithoutMainWithoutFailing(t *testing.T) {
	repo := gcRepo(t)
	remote := git(t, repo, "remote", "get-url", "origin")
	git(t, remote, "branch", "-m", "main", "master")
	path := filepath.Join(t.TempDir(), "outside")
	git(t, repo, "worktree", "add", "-b", "outside", path, "HEAD")
	ageGCTree(t, path)
	trees, err := GC(context.Background(), repo, GCOptions{})
	if err != nil {
		t.Fatalf("cleanup failed for a remote without main: %v", err)
	}
	kept := false
	for _, tree := range trees {
		if tree.Path == path {
			kept = tree.Action == "kept" && tree.Reason == "remote has no main branch"
		}
	}
	if !kept {
		t.Fatalf("outside tree not kept with the expected reason: %#v", trees)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestNewForRecordsExplicitIdentity(t *testing.T) {
	for _, tc := range []struct{ name, session, run string }{
		{"session", "verified-session", ""}, {"worker", "", "verified-run"}, {"owner", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := gcRepo(t)
			t.Setenv("DYNA_SESSION", "engine-session")
			t.Setenv("DYNA_RUN_ID", "engine-run")
			rec, err := NewFor(t.Context(), repo, "explicit-owner", "origin/main", tc.session, tc.run)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := Get(rec.Path)
			if err != nil || stored.Session != tc.session || stored.RunID != tc.run {
				t.Fatalf("record = %#v, %v", stored, err)
			}
		})
	}
}

func TestGCKeepsUnverifiedOwner(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "unverified-owner", "unverified")
	ageGCTree(t, path)
	trees, err := GC(t.Context(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		if tree.Path == path {
			if tree.Action != "kept" || !tree.OwnerOpen {
				t.Fatalf("unverified owner's worktree = %+v", tree)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("unverified owner's worktree is missing")
}

// A tree recorded for a run whose folder exists with no row and no logs
// stays: the run counts as open, not as an unknown owner.
func TestGCKeepsTreeOfRunWithoutRow(t *testing.T) {
	repo := gcRepo(t)
	path, _, _, err := CreateForRun(context.Background(), repo, settings.WorktreesRetentionNone, "wf_rowless", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runstore.RunsDir(), "wf_rowless"), 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour)
	for _, p := range []string{path, git(t, path, "rev-parse", "--absolute-git-dir")} {
		if err := filepath.Walk(p, func(p string, i os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			return os.Chtimes(p, old, old)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := GC(context.Background(), repo, GCOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tree of a run without a row was removed: %v", err)
	}
}
