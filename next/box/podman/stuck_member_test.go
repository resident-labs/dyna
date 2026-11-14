package podman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A group whose only member is a zombie (here an unreaped leader; on hosts a podman leader whose last thread waits on
// disk writeback) runs no more user code: cancellation confirms it instead of fencing every later host command.
func TestCancelConfirmsZombieOnlyGroup(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal(err)
		}
		if state := string(raw[len(fmt.Sprintf("%d (true) ", pid))]); state == "Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("leader never became a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	token := fmt.Sprintf("zombie-%d-%d", os.Getpid(), pid)
	dir := filepath.Join("/tmp", "dyna-start-"+token)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := &Host{runner: confirmationRunner{}, pendingCancels: map[string]struct{}{token: {}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.cancelRemote(ctx, token); err != nil {
		t.Fatalf("zombie-only group stayed unconfirmed: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("confirmed cancel kept its directory: %v", err)
	}
	if len(h.pendingCancels) != 0 {
		t.Fatal("confirmed cancel stayed pending")
	}
}
