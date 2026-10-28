//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"golang.org/x/sys/unix"
)

func TestResourceRunnerCrashHelper(t *testing.T) {
	if os.Getenv("RUNTIME_RESOURCE_CRASH_HELPER") != "1" {
		return
	}
	account := &Accounts{Root: os.Getenv("RESOURCE_ACCOUNT_ROOT")}
	bundles := &BundleManager{Root: os.Getenv("RESOURCE_BUNDLE_ROOT")}
	var bundle Bundle
	if err := json.Unmarshal([]byte(os.Getenv("RESOURCE_BUNDLE")), &bundle); err != nil {
		t.Fatal(err)
	}
	turn, err := account.TurnLeaseManaged(context.Background(), "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	binary, err := bundles.LeaseManaged(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	controller, err := processguard.New(context.Background(), "/bin/sh", "-c", `sleep 300 & echo "$!" > "$RESOURCE_CHILD_PID"; wait`)
	if err != nil {
		t.Fatal(err)
	}
	controller.Cmd().Env = append(os.Environ(), "RESOURCE_CHILD_PID="+os.Getenv("RESOURCE_READY"))
	if err = controller.Start(func(ref processguard.Reference) error {
		raw, e := json.Marshal(ref)
		if e != nil {
			return e
		}
		if e = turn.Bind(raw); e != nil {
			return e
		}
		return binary.Bind(raw)
	}); err != nil {
		t.Fatal(err)
	}
	if err = controller.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerCrashDrainsGrandchildBeforeResourcesBecomeUnused(t *testing.T) {
	accounts := testAccounts(t)
	bundles := manager(t)
	old := install(t, bundles, "pi", "1.3.0-dyna.6")
	_ = install(t, bundles, "pi", "2.0.0-dyna.6")
	_ = install(t, bundles, "pi", "3.0.0-dyna.6")
	raw, _ := json.Marshal(old)
	ready := filepath.Join(t.TempDir(), "grandchild.pid")
	// The helper writes its grandchild's PID into this pipe, which blocks
	// until the test reads it, so the PID is a real start signal.
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestResourceRunnerCrashHelper$")
	cmd.Env = append(os.Environ(), "RUNTIME_RESOURCE_CRASH_HELPER=1", "RESOURCE_ACCOUNT_ROOT="+accounts.Root, "RESOURCE_BUNDLE_ROOT="+bundles.Root, "RESOURCE_BUNDLE="+string(raw), "RESOURCE_READY="+ready)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	pids := make(chan []byte, 1)
	go func() {
		f, err := os.OpenFile(ready, os.O_RDONLY, 0)
		if err != nil {
			pids <- nil
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		pids <- b
	}()
	var pidRaw []byte
	select {
	case pidRaw = <-pids:
	case <-time.After(15 * time.Second):
		t.Fatal("native grandchild did not start", stderr.String())
	}
	if len(pidRaw) == 0 {
		t.Fatal("native grandchild did not report its PID")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := resourceLeasePaths(filepath.Join(accounts.Root, "pi", ".turns"), "")
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	live, use, err := resourceLeaseState(paths[0])
	if err != nil || !live {
		t.Fatal(live, err)
	}
	var ref processguard.Reference
	if err = json.Unmarshal(use.Reference, &ref); err != nil {
		t.Fatal(err)
	}
	defer processguard.Signal(ref, syscall.SIGKILL)
	// The guard kills, drains and reaps the native group before it exits, so
	// its exit reports finished cleanup without a clock sample.
	guardian, err := unix.PidfdOpen(ref.Guardian(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(guardian)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if release, e := accounts.switchGate(ctx, "pi"); e == nil {
		release()
		cancel()
		t.Fatal("live native turn did not block account switch")
	}
	cancel()
	if err = bundles.GC(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = bundles.Resolve("pi", old.Version); err != nil {
		t.Fatal("GC removed a live native bundle", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	// The runner is gone and its guard retires the native group, then exits.
	waitExit(t, guardian, "native group guard")
	if live, e := processguard.Probe(ref); e != nil || live {
		t.Fatalf("native group survived runner SIGKILL: live=%v, err=%v", live, e)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("backgrounded grandchild survived cleanup")
	}
	release, err := accounts.switchGate(context.Background(), "pi")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, running := accounts.SwitchStatus("pi"); running != 0 {
		t.Fatal("stale native turn retained the account lease", running)
	}
	if err = bundles.GC(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = bundles.Resolve("pi", old.Version); err == nil {
		t.Fatal("dead native process retained old bundle")
	}
}

// waitExit blocks until fd, a pidfd, reports that its process exited.
func waitExit(t *testing.T, fd int, what string) {
	t.Helper()
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	ready, err := unix.Poll(fds, 15000)
	for errors.Is(err, unix.EINTR) {
		ready, err = unix.Poll(fds, 15000)
	}
	if err != nil {
		t.Fatal(err)
	}
	if ready == 0 {
		t.Fatalf("%s did not exit", what)
	}
}
