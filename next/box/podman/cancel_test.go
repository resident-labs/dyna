package podman

import (
	"context"
	"errors"
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

type cancellationDebtRunner struct {
	t                 *testing.T
	cancel            context.CancelFunc
	first, confirm    bool
	commands, cancels int
}

func (r *cancellationDebtRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	if strings.Contains(command, "dyna-cancel") {
		r.cancels++
		if ctx.Err() != nil {
			r.t.Fatal("independent cancellation inherited cancelled context")
		}
		if !r.confirm {
			return nil, errors.New("independent SSH unavailable")
		}
		return nil, nil
	}
	r.commands++
	if r.first {
		r.first = false
		if r.cancel != nil {
			r.cancel()
		}
		return nil, errors.New("uncertain SSH reply")
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}
func TestUnconfirmedCancellationFencesSubsequentCleanup(t *testing.T) {
	for _, ownerCancel := range []bool{false, true} {
		t.Run(strconv.FormatBool(ownerCancel), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &cancellationDebtRunner{t: t, first: true}
			if ownerCancel {
				runner.cancel = cancel
			}
			h, err := New("user@host", t.TempDir(), "host", runner)
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.run(WithRemoteCancellation(ctx), "true")
			var unconfirmed RemoteCancellationUnconfirmed
			if !errors.As(err, &unconfirmed) || len(h.pendingCancels) != 1 {
				t.Fatalf("unconfirmed cancellation lost: %v, debts %d", err, len(h.pendingCancels))
			}
			commands := runner.commands
			_, err = h.run(context.Background(), "true")
			if !errors.As(err, &unconfirmed) || runner.commands != commands {
				t.Fatalf("cleanup passed unknown work: %v, commands %d -> %d", err, commands, runner.commands)
			}
			runner.confirm = true
			if _, err := h.run(context.Background(), "true"); err != nil || len(h.pendingCancels) != 0 || runner.commands != commands+1 {
				t.Fatalf("confirmed cleanup did not resume: %v, debts %d, commands %d", err, len(h.pendingCancels), runner.commands)
			}
		})
	}
}
