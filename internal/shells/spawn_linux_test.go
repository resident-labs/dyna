//go:build linux

package shells

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/procident"
)

// Some harnesses kill an MCP server's whole live process tree when a run
// ends, so the supervisor must not stay a descendant of its caller.
func TestSpawnedSupervisorIsNotADescendantOfTheCaller(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(ready, 0600); err != nil {
		t.Fatal(err)
	}
	// Read-write, so the open never blocks and a writer is always present.
	fd, err := unix.Open(ready, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	t.Setenv("SPAWN_READY", ready)
	if err := Spawn(os.Args[0], testJob(t)); err != nil {
		t.Fatal(err)
	}
	n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 900)
	for err == unix.EINTR {
		n, err = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 900)
	}
	if err != nil || n == 0 {
		t.Fatalf("the supervisor did not report its PID: %v", err)
	}
	buf := make([]byte, 32)
	m, err := unix.Read(fd, buf)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:m])))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	// Field 4, after the parenthesized command name, is the parent PID.
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	if ppid, _ := strconv.Atoi(fields[1]); ppid == os.Getpid() {
		t.Fatalf("supervisor %d is still a child of the caller %d", pid, ppid)
	}
}

func TestSpawnReportsSupervisorLaunchErrors(t *testing.T) {
	notExecutable := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := testJob(t)
	// The launcher itself cannot start.
	if err := Spawn(filepath.Join(t.TempDir(), "missing"), id); err == nil {
		t.Fatal("a missing launcher reported success")
	}
	// The detached launcher starts, but the supervisor cannot.
	t.Setenv("SPAWN_INNER", notExecutable)
	if err := Spawn(os.Args[0], id); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("a non-executable supervisor gave %v", err)
	}
}

func TestSupervisorErrorsAreKeptInTheJobFolder(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := DefaultStore()
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(ready, 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(ready, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	// A supervisor that fails before it records anything.
	exe := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'database is locked' >&2\necho done > \""+ready+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := StartSupervisor(exe, meta.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 900); err != nil || n == 0 {
		t.Fatalf("the supervisor did not finish: %v", err)
	}
	if got := s.SupervisorErrors(meta.ID); got != "database is locked" {
		t.Fatalf("supervisor errors = %q", got)
	}
}

// testJob records a job in a fresh default store and returns its ID.
func testJob(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	meta, err := DefaultStore().Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return meta.ID
}

// A supervisor that is alive but still waiting for dyna.db's write lock has
// not recorded itself. Its job must read as starting, however old, and as
// lost only once that process is gone.
func TestBlockedSupervisorIsStartingNotLost(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	meta.Started = time.Now().Add(-time.Minute)
	if err := s.Save(meta, nil); err != nil {
		t.Fatal(err)
	}
	waiting := exec.Command("sleep", "600")
	if err := waiting.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = waiting.Process.Kill(); _ = waiting.Wait() })
	started, err := procident.StartTime(waiting.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.dir(meta.ID)
	if err := os.WriteFile(filepath.Join(dir, supervisorPID), []byte(strconv.Itoa(waiting.Process.Pid)+" "+strconv.FormatUint(started, 10)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "starting" {
		t.Fatalf("blocked supervisor job = %q, %v", in.Status, err)
	}
	if err := waiting.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = waiting.Wait()
	if in, err := s.Info(meta.ID); err != nil || in.Status != "lost" {
		t.Fatalf("dead supervisor job = %q, %v", in.Status, err)
	}
}

// A stop that arrives while the supervisor waits to record itself wins: the
// command never runs.
func TestStopBeforeStartNeverRunsTheCommand(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	marker := filepath.Join(t.TempDir(), "ran")
	meta, err := s.Create(StartOptions{Cmd: []string{"touch", marker}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Supervise(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the command ran after the stop: %v", err)
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "exited" || in.Exit.Error != "stopped before start" {
		t.Fatalf("job = %#v, %v", in, err)
	}
}
