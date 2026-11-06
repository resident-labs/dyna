package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type remoteCancelUnavailable struct{ fail bool }

func (r *remoteCancelUnavailable) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	if strings.Contains(args[len(args)-1], "dyna-cancel") && r.fail {
		return nil, errors.New("independent cancellation connection unavailable")
	}
	return (ExecRunner{}).Run(ctx, in, "bash", "-c", `setsid bash -c "$1" <&0 & wait`, "remote-ssh", args[len(args)-1])
}

func remoteAwaitFile(t *testing.T, path string) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("file never appeared: %s", path)
}

func TestUnconfirmedCancelMustNotPermitSuccessfulCleanupWhileCreateLives(t *testing.T) {
	dir := t.TempDir()
	entered, release, resource := filepath.Join(dir, "entered"), filepath.Join(dir, "release"), filepath.Join(dir, "resource")
	script := `#!/bin/bash
shift
case "$1" in
create)
 touch "$REMOTE_ENTERED"
 while [[ ! -f $REMOTE_RELEASE ]]; do sleep .01; done
 touch "$REMOTE_RESOURCE"
 ;;
rm) rm -f "$REMOTE_RESOURCE" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE_ENTERED", entered)
	t.Setenv("REMOTE_RELEASE", release)
	t.Setenv("REMOTE_RESOURCE", resource)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	r := &remoteCancelUnavailable{fail: true}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- h.CreateContainer(WithRemoteCancellation(ctx), box.ContainerSpec{Box: contract.BoxID("b"), Image: "image"})
	}()
	remoteAwaitFile(t, entered)
	cancel()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "cancellation unconfirmed") {
		t.Fatalf("expected unconfirmed cancellation, got %v", err)
	}
	// The cancel connection recovers; a pending-token preflight could retry confirmation now.
	r.fail = false
	if err := h.RemoveContainer(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(end) {
		if _, err := os.Stat(resource); err == nil {
			t.Fatal("RemoveContainer reported success while prior unconfirmed create remained live; container resource was created after cleanup")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWrapperExitMustNotEraseOwnershipOfLiveGroup(t *testing.T) {
	token := fmt.Sprintf("remote-wrapper-exit-%d", time.Now().UnixNano())
	dir := filepath.Join("/tmp", "dyna-start-"+token)
	entered, release := filepath.Join(t.TempDir(), "entered"), filepath.Join(t.TempDir(), "release")
	cmd := exec.Command("bash", "-c", cancellableFrameScript, "dyna-frame", token, "bash", "-c", `touch "$1"; while [[ ! -f $2 ]]; do sleep .01; done`, "remote-worker", entered, release)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(fmt.Sprintf("DYNA-IN %020d\n", 0))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait(); _ = os.RemoveAll(dir) }()
	remoteAwaitFile(t, entered)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	out, err := exec.Command("bash", "-c", cancelRemoteScript, "dyna-cancel", token).CombinedOutput()
	if err != nil {
		t.Fatalf("cancel: %v %s", err, out)
	}
	// Any remaining live group members demonstrate that the wrapper's exit trap erased the record too soon.
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		cut := strings.LastIndex(string(raw), ") ")
		if cut < 0 {
			continue
		}
		f := strings.Fields(string(raw[cut+2:]))
		if len(f) < 3 {
			continue
		}
		if f[2] == fmt.Sprint(cmd.Process.Pid) && f[0] != "Z" && f[0] != "X" {
			t.Errorf("cancel returned success after wrapper termination, but group member %s is live in state %s", entry.Name(), f[0])
		}
	}
}
