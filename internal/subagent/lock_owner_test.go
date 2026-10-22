package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// This subprocess receives the lock exactly as _turn does, then starts a
// background shell. The shell must remain alive after its parent exits.
func TestTurnLockChildHelper(t *testing.T) {
	// TestMain clears DYNA_* settings before helper tests run.
	if os.Getenv("GO_WANT_TURN_LOCK_CHILD") != "1" {
		return
	}
	lock := os.NewFile(3, "turn.lock")
	protectLock(lock)
	child := exec.Command("sh", "-c", "exec sleep 30")
	child.Stdout, child.Stderr = nil, nil
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	fmt.Println(child.Process.Pid)
	_ = child.Process.Release()
	os.Exit(0)
}

func TestWakeRecoversLockHeldByNonRunner(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, turn := e.start("finish first turn")
	e.wait(info.ID, turn.N)
	e.waitStatus(info.ID, StatusIdle)
	lock, err := e.m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	child := exec.Command("sleep", "30")
	child.ExtraFiles = []*os.File{lock}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	lock.Close()
	dir, _ := e.m.Store.Dir(info.ID)
	if err := writeGateState(dir, gateState{Pid: child.Process.Pid, Turn: turn.N}); err != nil {
		t.Fatal(err)
	}
	if e.m.Store.lockHeld(info.ID) {
		t.Fatal("a non-runner must not make a session running")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	result, err := e.m.Wake(ctx, info.ID, WakeOptions{continuation: "follow up"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Turn != turn.N+1 {
		t.Fatalf("wake = %+v; expected follow-up", result)
	}
	e.wait(info.ID, result.Turn)
	// Recovery replaces the lock inode. The old shell stays alive throughout.
	if err := syscall.Kill(child.Process.Pid, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "turn.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestWakeDoesNotWaitForUnidentifiedLockHolder(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, turn := e.start("finish first turn")
	e.wait(info.ID, turn.N)
	e.waitStatus(info.ID, StatusIdle)
	lock, err := e.m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	dir, _ := e.m.Store.Dir(info.ID)
	if err := writeGateState(dir, gateState{Turn: turn.N}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	_, err = e.m.Wake(ctx, info.ID, WakeOptions{})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("wake error = %v", err)
	}
}

func TestRetiredTurnLockCannotBeAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turn.lock")
	old, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if !turnLockMatchesPath(old, path) {
		t.Fatal("current lock rejected")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	current, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if turnLockMatchesPath(old, path) {
		t.Fatal("retired lock inode accepted")
	}
	if !turnLockMatchesPath(current, path) {
		t.Fatal("replacement lock rejected")
	}
}
