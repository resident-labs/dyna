package subagent

import (
	"context"
	"path/filepath"
	"testing"

	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktrees/worktreetest"
)

// A launch tree that was removed after its PR merged is never reused. The
// packet continues in the session's newest retained tree instead.
func TestHandoffPacketFallsBackToRetainedTreeAfterLaunchTreeRemoved(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("RESPOND: build the parser")
	gone := filepath.Join(t.TempDir(), "removed")
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Dir = gone }); err != nil {
		t.Fatal(err)
	}
	tree := worktreetest.Tracked(t)
	if err := worktrees.Track(worktrees.Record{Path: tree, Session: info.ID}); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, dir, err := e.m.handoffPacket(meta, TransferState{Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if dir != tree {
		t.Fatalf("transfer used %q, want the retained tree %q", dir, tree)
	}
}

// The root override names the task directory of a fresh conversation. A
// directory that does not exist is refused before the transfer starts.
func TestTransferDirOverrideMustExist(t *testing.T) {
	e, _ := transferEnv(t)
	info, _ := e.start("RESPOND: original work")
	info, _ = e.wait(info.ID, 1)
	ctx := context.Background()
	if _, err := e.m.TransferContext(ctx, info.ID, TransferOptions{Profile: "next", Dir: filepath.Join(t.TempDir(), "gone"), WaitSeconds: 30}); err == nil {
		t.Fatal("transfer accepted a task directory that does not exist")
	}
	tree := worktreetest.Tracked(t)
	state, err := e.m.TransferContext(ctx, info.ID, TransferOptions{Profile: "next", Dir: tree, WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if state.Dir != tree {
		t.Fatalf("transfer dir = %q, want the override %q", state.Dir, tree)
	}
}
