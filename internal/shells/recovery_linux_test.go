//go:build linux

package shells

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/procident"
)

func recoveryProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestStopExitedSupervisor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		identity   string
		missingLog bool
		gone       bool
		wantError  string
		wantKilled bool
	}{
		{name: "matching identity", wantKilled: true},
		{name: "legacy identity", identity: "missing", wantError: "cannot verify supervisor"},
		{name: "reused PID", identity: "changed", wantError: "cannot verify supervisor"},
		{name: "missing log", missingLog: true, wantKilled: true, wantError: "output.log"},
		{name: "supervisor already gone", gone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{Root: t.TempDir()}
			meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			supervisor := recoveryProcess(t)
			unrelated := recoveryProcess(t)
			meta.Supervisor = supervisor.Process.Pid
			// A stale command PID must never be signaled after the exit is recorded.
			meta.Pid = unrelated.Process.Pid
			meta.SupervisorStartTime, err = procident.StartTime(meta.Supervisor)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.identity {
			case "missing":
				meta.SupervisorStartTime = 0
			case "changed":
				meta.SupervisorStartTime++
			}
			if tc.gone {
				if err := supervisor.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = supervisor.Wait()
			}
			dir, _ := s.dir(meta.ID)
			exit := Exit{Code: 7, Ended: time.Now().UTC()}
			if err := s.Save(meta, &exit); err != nil {
				t.Fatal(err)
			}
			if tc.missingLog {
				if err := os.Remove(filepath.Join(dir, "output.log")); err != nil {
					t.Fatal(err)
				}
			}
			in, err := s.Stop(meta.ID, "owner")
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("Stop error = %v, want %q", err, tc.wantError)
			}
			if in.Status != "exited" || in.Exit == nil || in.Exit.Code != 7 {
				t.Fatalf("Stop lost exit status: %#v", in)
			}
			if after, err := s.ReadExit(meta.ID); err != nil || after == nil || *after != exit {
				t.Fatalf("exit record changed: %#v, %v", after, err)
			}
			if tc.wantKilled {
				if err := supervisor.Wait(); err == nil {
					t.Fatal("supervisor was not killed")
				}
				if st, ok := supervisor.ProcessState.Sys().(syscall.WaitStatus); !ok || !st.Signaled() || st.Signal() != syscall.SIGKILL {
					t.Fatalf("supervisor exit = %v", supervisor.ProcessState)
				}
			} else if !tc.gone {
				assertRecoveryProcessAlive(t, supervisor.Process.Pid)
			}
			assertRecoveryProcessAlive(t, unrelated.Process.Pid)
		})
	}
}

func assertRecoveryProcessAlive(t *testing.T, pid int) {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0)
	if err != nil || n != 0 {
		t.Fatalf("process %d exited unexpectedly: poll = %d, %v", pid, n, err)
	}
}

func TestSuperviseRecordsIdentityBeforeCommandStartFailure(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	meta, err := s.Create(StartOptions{Cmd: []string{filepath.Join(t.TempDir(), "missing-command")}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Supervise(context.Background(), meta.ID); err == nil {
		t.Fatal("expected command launch failure")
	}
	meta, err = s.ReadMeta(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	started, err := procident.StartTime(os.Getpid())
	if err != nil || meta.Supervisor != os.Getpid() || meta.SupervisorStartTime == 0 || meta.SupervisorStartTime != started {
		t.Fatalf("supervisor identity = %d/%d, want %d/%d: %v", meta.Supervisor, meta.SupervisorStartTime, os.Getpid(), started, err)
	}
}
