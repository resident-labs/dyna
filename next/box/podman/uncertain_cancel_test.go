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

func TestUnconfirmedCancelMustNotPermitSuccessfulCleanupWhileCreateLives(t *testing.T) {
	dir := t.TempDir()
	entered, release := newScriptSignal(t), newScriptSignal(t)
	resource := filepath.Join(dir, "resource")
	script := `#!/bin/bash
shift
case "$1" in
create)
 printf '%s %s\n' "$$" "$(/usr/bin/ps -o pgid= -p "$$")" > "$REMOTE_ENTERED"
 IFS= read -r _ < "$REMOTE_RELEASE"
 touch "$REMOTE_RESOURCE"
 ;;
rm) rm -f "$REMOTE_RESOURCE" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE_ENTERED", entered.path)
	t.Setenv("REMOTE_RELEASE", release.path)
	t.Setenv("REMOTE_RESOURCE", resource)
	t.Cleanup(func() { release.write(t, "go") })
	r := &remoteCancelUnavailable{fail: true}
	h, err := New("user@host", t.TempDir(), "host", r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- h.CreateContainer(WithRemoteCancellation(ctx), box.ContainerSpec{Box: contract.BoxID("b"), Image: "image"})
	}()
	_, group := entered.process(t)
	defer syscall.Kill(-group, syscall.SIGKILL)
	cancel()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "cancellation unconfirmed") {
		t.Fatalf("expected unconfirmed cancellation, got %v", err)
	}
	// The cancel connection recovers; a pending-token preflight could retry confirmation now.
	r.fail = false
	if err := h.RemoveContainer(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	assertRemoteGroupExited(t, group)
	release.write(t, "go")
	if _, err := os.Stat(resource); !os.IsNotExist(err) {
		t.Fatalf("cleanup returned while a prior create could still create its resource: %v", err)
	}
}

func TestWrapperExitMustNotEraseOwnershipOfLiveGroup(t *testing.T) {
	token := fmt.Sprintf("remote-wrapper-exit-%d", time.Now().UnixNano())
	dir := filepath.Join("/tmp", "dyna-start-"+token)
	entered, release := newScriptSignal(t), newScriptSignal(t)
	cmd := exec.Command("bash", "-c", cancellableFrameScript, "dyna-frame", token, "bash", "-c", `printf '%s %s\n' "$$" "$(/usr/bin/ps -o pgid= -p "$$")" > "$1"; IFS= read -r _ < "$2"`, "remote-worker", entered.path, release.path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(fmt.Sprintf("DYNA-IN %020d\n", 0))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap the leader we own concurrently; an unreaped test-owned zombie is
	// still a group member to procps and must not be exempted by production.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); <-done; _ = os.RemoveAll(dir) }()
	pid, group := entered.process(t)
	if group != cmd.Process.Pid {
		t.Fatalf("payload group %d, want wrapper %d", group, cmd.Process.Pid)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper did not exit after SIGTERM")
	}
	if !remoteLivePID(t, fmt.Sprint(pid)) {
		t.Fatal("fixture payload did not survive wrapper exit")
	}
	out, err := exec.Command("bash", "-c", cancelRemoteScript, "dyna-cancel", token).CombinedOutput()
	if err != nil {
		t.Fatalf("cancel: %v %s", err, out)
	}
	assertRemoteGroupExited(t, group)
}
