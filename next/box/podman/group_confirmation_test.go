package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Execute the actual cancellation payload locally, shortening only its polling
// budget. KILL is suppressed in the alive case so the real group remains live.
// The status cases substitute only the procps boundary, never the Go ownership logic.
type confirmationRunner struct {
	status, log      string
	alive, transport bool
}

func (r confirmationRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	if r.transport {
		return nil, errors.New("independent cancellation transport unavailable")
	}
	script := strings.Replace(cancelRemoteScript, "SECONDS+20", "SECONDS+1", 1)
	if r.alive {
		script = strings.Replace(script, "kill -KILL --", "false --", 1)
	}
	if r.status != "" {
		script = fmt.Sprintf("pgrep() { printf '%%s\\n' \"$*\" >> %s; echo 'untrusted diagnostic: no such process' >&2; return %s; }\n", quote(r.log), r.status) + script
	}
	remote := strings.Replace(args[len(args)-1], quote(cancelRemoteScript), quote(script), 1)
	return (ExecRunner{}).Run(ctx, in, "bash", "-c", remote)
}

func TestConfirmationProcessGroupStatusOwnership(t *testing.T) {
	for _, status := range []string{"0", "1", "2", "3", "42", "127", "transport"} {
		t.Run(status, func(t *testing.T) {
			// No children: the test owns and concurrently reaps its group leader.
			cmd := exec.Command("sleep", "1000")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); <-done })
			token := fmt.Sprintf("confirm-%d-%d", os.Getpid(), cmd.Process.Pid)
			dir := filepath.Join("/tmp", "dyna-start-"+token)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			pid := strconv.Itoa(cmd.Process.Pid)
			if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(pid+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			owned := filepath.Join(t.TempDir(), "owned temp\nwith newline")
			unrelated := filepath.Join(t.TempDir(), "unrelated")
			for _, name := range []string{owned, unrelated} {
				if err := os.WriteFile(name, []byte("keep until confirmed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "temps"), []byte(owned+"\x00"), 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "pgrep-args")
			r := confirmationRunner{status: status, log: log, alive: status == "0", transport: status == "transport"}
			h := &Host{runner: r, pendingCancels: map[string]struct{}{token: {}}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := h.cancelRemote(ctx, token)
			if ctx.Err() != nil {
				t.Fatalf("test deadline expired, not a confirmation assertion: %v", ctx.Err())
			}
			_, pending := h.pendingCancels[token]
			t.Logf("pgrep status %s: cancel=%v pending=%v", status, err, pending)
			var unconfirmed RemoteCancellationUnconfirmed
			if status == "1" {
				if err != nil || pending {
					t.Errorf("gone group must clear pending ownership: err=%v pending=%v", err, pending)
				}
				for _, name := range []string{dir, owned} {
					if _, err := os.Stat(name); !os.IsNotExist(err) {
						t.Errorf("confirmed gone group retained %s: %v", name, err)
					}
				}
			} else {
				if !errors.As(err, &unconfirmed) || !pending {
					t.Errorf("uncertain group must retain pending ownership: err=%v pending=%v", err, pending)
				}
				for _, name := range []string{filepath.Join(dir, "pid"), filepath.Join(dir, "temps"), owned} {
					if _, err := os.Stat(name); err != nil {
						t.Errorf("uncertain group lost %s: %v", name, err)
					}
				}
			}
			if _, err := os.Stat(unrelated); err != nil {
				t.Errorf("unrelated file affected: %v", err)
			}
			if status == "transport" {
				return
			}
			if status != "1" {
				if _, err := os.Stat(filepath.Join(dir, "cancel")); err != nil {
					t.Errorf("uncertain group lost cancellation fence: %v", err)
				}
			}
			calls, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatalf("pgrep boundary was not called: %v", readErr)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			for _, line := range lines {
				if line != "-g "+pid {
					t.Errorf("expected positive PGID, got pgrep %q", line)
				}
			}
			if status == "0" {
				if len(lines) < 2 {
					t.Error("alive status must continue polling before uncertainty")
				}
				// Verify the alive fixture is real, independently of the injected boundary.
				if err := exec.Command("pgrep", "-g", pid).Run(); err != nil {
					t.Fatalf("fixture group is not alive: %v", err)
				}
			} else if len(lines) != 1 {
				t.Errorf("terminal status %s unexpectedly polled %d times", status, len(lines))
			}
		})
	}
}

func TestConfirmationRejectsNonPositivePGID(t *testing.T) {
	for _, pid := range []string{"0", "000", "-1", "", "not-a-pid"} {
		t.Run(fmt.Sprintf("pid_%q", pid), func(t *testing.T) {
			token := fmt.Sprintf("invalid-pgid-%d", time.Now().UnixNano())
			dir := filepath.Join("/tmp", "dyna-start-"+token)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(pid+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Guard the test process group even against a regressed validation gate.
			log := filepath.Join(t.TempDir(), "calls")
			script := "kill() { echo kill >> " + quote(log) + "; }; pgrep() { echo pgrep >> " + quote(log) + "; return 1; };\n" + cancelRemoteScript
			out, err := exec.Command("bash", "-c", script, "dyna-cancel", token).CombinedOutput()
			if err == nil {
				t.Errorf("invalid PGID was confirmed: %q %s", pid, out)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Errorf("invalid PGID reached kill/pgrep: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "pid")); err != nil {
				t.Errorf("invalid PGID lost ownership: %v", err)
			}
		})
	}
}
