package podman

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Execute the SSH payload locally while preserving ExecRunner's kill-and-join behavior. The command's setsid child is not
// killed by killing the local SSH stand-in, so only the remote cancellation protocol can stop it.
type nativeShellRunner struct{}

func (nativeShellRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	return (ExecRunner{}).Run(ctx, in, "bash", "-c", args[len(args)-1])
}
func TestCancelledStartKillsRemoteProcessBeforeCleanup(t *testing.T) {
	h, err := New("user@host", t.TempDir(), "host", nativeShellRunner{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	entered := filepath.Join(dir, "entered")
	late := filepath.Join(dir, "late")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.run(WithRemoteCancellation(ctx), "bash", "-c", `echo $$ > "$1"; sleep 10; touch "$2"`, "dyna-test", entered, late)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	var pidBytes []byte
	for time.Now().Before(deadline) {
		pidBytes, err = os.ReadFile(entered)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("command never entered")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not join")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	// A zombie has no work left to run; an extant non-zombie command would still be able to create the late resource.
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if stat := strings.TrimSpace(string(out)); stat != "" && !strings.HasPrefix(stat, "Z") {
		t.Fatalf("remote process survived cancellation: %s", stat)
	}
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatalf("late resource: %v", err)
	}
}
