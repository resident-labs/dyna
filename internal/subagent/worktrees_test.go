package subagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/worktrees"
)

func lifecycleGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// lifecycleRepository needs local branch ancestry, not a remote transport.
func lifecycleRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	lifecycleGit(t, repo, "init", "-b", "main")
	lifecycleGit(t, repo, "commit", "--allow-empty", "-m", "base")
	lifecycleGit(t, repo, "remote", "add", "origin", repo)
	lifecycleGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	return repo
}

// lifecycleOwnedWorktree builds the real Git fixture directly. These tests
// exercise lifecycle cleanup; allocation and fetch behavior has its own tests.
func lifecycleOwnedWorktree(t *testing.T, repo, root, branch, session string) worktrees.Record {
	t.Helper()
	path := filepath.Join(root, branch)
	lifecycleGit(t, repo, "worktree", "add", "-b", branch, path, "origin/main")
	rec := worktrees.Record{Path: path, Repo: repo, Branch: branch, Base: "origin/main", Session: session, CreatedAt: time.Now().UTC()}
	if err := worktrees.Track(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestLifecycleCleansOnlySafeOwnedWorktrees(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "top-level", true: "child"}[child], func(t *testing.T) {
			e := newTestEnv(t)
			e.joinRunners()
			info, _ := e.start("RESPOND: ready SLEEP:1")
			info = waitFixtureTurn(t, e, info)
			parent := info
			if child {
				stampParent(t, parent)
				info, _ = e.start("RESPOND: child SLEEP:1")
				info = waitFixtureTurn(t, e, info)
			}
			root := t.TempDir()
			v := settings.Defaults()
			if err := v.Set(settings.KeyWorktreesRoot, root); err != nil {
				t.Fatal(err)
			}
			if err := v.Save(); err != nil {
				t.Fatal(err)
			}
			repo := lifecycleRepository(t)
			clean := lifecycleOwnedWorktree(t, repo, root, "clean", info.ID)
			dirty := lifecycleOwnedWorktree(t, repo, root, "dirty", info.ID)
			os.WriteFile(filepath.Join(dirty.Path, "untracked"), []byte("preserve"), 0600)
			ahead := lifecycleOwnedWorktree(t, repo, root, "ahead", info.ID)
			os.WriteFile(filepath.Join(ahead.Path, "commit"), []byte("unlanded"), 0600)
			lifecycleGit(t, ahead.Path, "add", ".")
			lifecycleGit(t, ahead.Path, "commit", "-m", "unlanded")
			e.doneGoal(info)
			if _, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true}); err != nil {
				t.Fatal(err)
			}
			awaitWorktreeCleanup(t, e.m)
			if _, err := os.Stat(clean.Path); !os.IsNotExist(err) {
				t.Fatalf("clean tree survived: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dirty.Path, "untracked")); err != nil {
				t.Fatalf("dirty file lost: %v", err)
			}
			for _, path := range []string{dirty.Path, ahead.Path} {
				rec, err := worktrees.Get(path)
				if err != nil || rec.Session != info.ID {
					t.Fatalf("ownership changed: %+v, %v", rec, err)
				}
			}
			lifecycleGit(t, repo, "show-ref", "--verify", "refs/heads/clean", "refs/heads/ahead")
			posts, err := e.m.rooms().Messages(info.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, p := range posts {
				if p.Kind == string(room.FamilyWorktree) {
					found++
					if p.From != room.EngineSender || p.Wake || len(p.To) != 1 || p.To[0] != info.Address() || p.Deliverable() {
						t.Fatalf("worktree notice must be quiet, from the engine, to the owner session only: %+v", p)
					}
					kept, ok := p.Worktree()
					if !ok || kept.Path != dirty.Path && kept.Path != ahead.Path {
						t.Fatalf("notice payload names no kept tree: %+v", p.Payload)
					}
					if kept.Reason != room.WorktreeDirty && kept.Reason != room.WorktreeNotMerged {
						t.Fatalf("notice reason = %q", kept.Reason)
					}
				}
				if strings.Contains(p.Text, "Worktree cleanup kept:") {
					t.Fatalf("untyped cleanup text: %+v", p)
				}
			}
			if found != 2 {
				t.Fatalf("cleanup notices=%d, want one per kept tree", found)
			}
			if child {
				if meta, err := e.m.Store.ReadMeta(parent.ID); err != nil || meta.Turns != 1 {
					t.Fatalf("cleanup notice woke the parent: %+v, %v", meta, err)
				}
			}
		})
	}
}

// A kept tree is reported once per tree and reason, to its owner session,
// even when a later pass finds it again. The reviewer that closes is not the
// sender, and the post never wakes anyone.
func TestKeptWorktreeNoticeIsPostedOnce(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	owner, _ := e.start("SLEEP:1 RESPOND: owner")
	owner, _ = e.wait(owner.ID, 1)
	closing, _ := e.start("SLEEP:1 RESPOND: closing")
	closing, _ = e.wait(closing.ID, 1)
	k := keptTree{Owner: owner.ID, Path: "/w/feature", Branch: "feature", Ahead: 1, Reason: worktrees.ReasonNotMerged + "; cleanup kept it"}
	for range 3 {
		e.m.reportKeptWorktree(closing, k)
	}
	k.Ahead = 2 // the same tree and reason, new facts
	e.m.reportKeptWorktree(closing, k)
	posts, err := e.m.rooms().Messages(closing.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	var got []room.Message
	for _, p := range posts {
		if p.Kind == string(room.FamilyWorktree) {
			got = append(got, p)
		}
	}
	if len(got) != 1 || got[0].From != room.EngineSender || len(got[0].To) != 1 || got[0].To[0] != owner.Address() || got[0].Wake {
		t.Fatalf("kept notices = %+v", got)
	}
	if got[0].Conversation != room.DMConversation(owner.ID) {
		t.Fatalf("kept notice conversation = %q, want the owner session's DM", got[0].Conversation)
	}
	kept, ok := got[0].Worktree()
	if !ok || kept.Owner != owner.ID || kept.Path != "/w/feature" || kept.Reason != room.WorktreeNotMerged || kept.Ahead != 1 {
		t.Fatalf("kept payload = %+v, %v", kept, ok)
	}
}

func TestFinishedKeepsCleanWorktreeUntilClose(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	root := t.TempDir()
	v := settings.Defaults()
	if err := v.Set(settings.KeyWorktreesRoot, root); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	repo := lifecycleRepository(t)
	tree := lifecycleOwnedWorktree(t, repo, root, "done-tree", info.ID)
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Dir = tree.Path }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tree.Path); err != nil {
		t.Fatalf("done worktree removed: %v", err)
	}
	if closed, err := worktrees.SessionClosed(info.ID); err != nil || closed {
		t.Fatalf("done owner lost protection: %v %v", closed, err)
	}
	// General GC must honor the done owner, including inferred ownership.
	if _, err := worktrees.GC(e.ctx, repo, worktrees.GCOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tree.Path); err != nil {
		t.Fatalf("GC removed done worktree: %v", err)
	}
	if _, err := e.send(info.ID, "RESPOND: resumed SLEEP:1", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	e.wait(info.ID, 2)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Close(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	awaitWorktreeCleanup(t, e.m)
	if _, err := os.Stat(tree.Path); !os.IsNotExist(err) {
		t.Fatalf("dismissed clean worktree kept: %v", err)
	}
	lifecycleGit(t, repo, "show-ref", "--verify", "refs/heads/done-tree")
}

func awaitWorktreeCleanup(t *testing.T, m *Manager) {
	t.Helper()
	if left := m.workers().Wait(5 * time.Second); len(left) > 0 {
		t.Fatalf("worktree cleanup still running: %v", left)
	}
}

// Close commits, then cleanup runs on the engine worker. Stopping that
// worker leaves a clean merged tree in place and posts no false failure.
func TestCloseReturnsWhileCleanupIsBlockedAndStopKeepsWorktrees(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	root := t.TempDir()
	v := settings.Defaults()
	if err := v.Set(settings.KeyWorktreesRoot, root); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	repo := lifecycleRepository(t)
	clean := lifecycleOwnedWorktree(t, repo, root, "clean", info.ID)
	dirty := lifecycleOwnedWorktree(t, repo, root, "dirty", info.ID)
	if err := os.WriteFile(filepath.Join(dirty.Path, "untracked"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	workers := NewWorkers(ctx, nil)
	e.m.Workers = workers
	t.Cleanup(func() {
		cancel()
		_ = workers.Wait(time.Second)
	})
	started := make(chan struct{})
	e.m.checkpoint = func(stage string) {
		if stage != "cleanup-start" {
			return
		}
		close(started)
		<-ctx.Done()
	}
	if _, err := e.m.Close(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || meta.Closed.IsZero() {
		t.Fatalf("close was not durable while cleanup was blocked: %+v, %v", meta, err)
	}
	if left := workers.Wait(10 * time.Millisecond); len(left) != 1 || left[0] != "cleanup "+info.ID {
		t.Fatalf("cleanup worker while blocked = %v", left)
	}
	for _, path := range []string{clean.Path, filepath.Join(dirty.Path, "untracked")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("tree changed while cleanup was blocked: %s: %v", path, err)
		}
	}
	cancel()
	stopped := time.Now()
	if left := workers.Wait(time.Second); len(left) > 0 {
		t.Fatalf("cleanup still running after engine stop: %v", left)
	}
	if took := time.Since(stopped); took > 500*time.Millisecond {
		t.Fatalf("engine stop waited %v for worktree cleanup", took)
	}
	if _, err := os.Stat(clean.Path); err != nil {
		t.Fatalf("clean tree removed after stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirty.Path, "untracked")); err != nil {
		t.Fatalf("dirty file lost after stop: %v", err)
	}
	kept, err := worktrees.Get(dirty.Path)
	if err != nil || kept.Session != info.ID {
		t.Fatalf("dirty ownership changed: %+v, %v", kept, err)
	}
	lifecycleGit(t, repo, "show-ref", "--verify", "refs/heads/clean", "refs/heads/dirty")
	posts, err := e.m.rooms().Messages(info.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, post := range posts {
		if strings.Contains(post.Text, "Worktree cleanup kept:") {
			t.Fatalf("stop posted a cleanup failure: %s", post.Text)
		}
	}
	meta, err = e.m.Store.ReadMeta(info.ID)
	if err != nil || meta.Closed.IsZero() {
		t.Fatalf("stop reopened the session: %+v, %v", meta, err)
	}
}

// goroutineID reads the current goroutine's id from its stack header.
func goroutineID() int {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	rest := buf[:n]
	const prefix = "goroutine "
	rest = rest[len(prefix):]
	id := 0
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + int(c-'0')
	}
	return id
}

// A manager outside the engine has no Workers. Cleanup then runs on the close
// call and finishes before Close returns. A process-lifetime worker would
// still be writing when the caller removes its data directory.
func TestCloseWithoutWorkersFinishesCleanupBeforeReturn(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	e.m.Workers = nil
	started := make(chan int, 1)
	block, release := context.WithCancel(context.Background())
	t.Cleanup(release)
	e.m.checkpoint = func(stage string) {
		if stage != "cleanup-start" {
			return
		}
		started <- goroutineID()
		<-block.Done()
	}
	returned := make(chan error, 1)
	closeID := 0
	go func() {
		closeID = goroutineID()
		_, err := e.m.Close(e.ctx, info.ID)
		returned <- err
	}()
	var cleanupID int
	select {
	case cleanupID = <-started:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	if cleanupID == 0 || cleanupID != closeID {
		t.Fatalf("cleanup ran on goroutine %d, close on %d", cleanupID, closeID)
	}
	select {
	case err := <-returned:
		t.Fatalf("Close returned before cleanup finished: %v", err)
	default:
	}
	release()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after cleanup finished")
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || meta.Closed.IsZero() {
		t.Fatalf("close was not durable after cleanup: %+v, %v", meta, err)
	}
}
