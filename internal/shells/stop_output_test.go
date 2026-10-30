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
)

// D-210: a command that ends by itself while a child still holds its output
// open. Nothing stops it later, so the supervisor alone must record the exit.
func TestSuperviseRecordsExitOfCommandThatLeftAChildHoldingOutput(t *testing.T) {
	ready := make(chan time.Time)
	close(ready)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells"), afterStop: func(time.Duration) <-chan time.Time { return ready }}
	fifo := filepath.Join(t.TempDir(), "pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := s.Create(StartOptions{Cmd: []string{"sh", "-c", "setsid sleep 600 & echo $! > " + fifo + "; exit 3"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var holder int
	s.OnStarted = func(int) {
		raw, err := os.ReadFile(fifo) // blocks until the command writes the holder's PID
		if err != nil {
			t.Error(err)
		} else if holder, err = strconv.Atoi(strings.TrimSpace(string(raw))); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() {
		if holder > 0 {
			_ = syscall.Kill(holder, syscall.SIGKILL)
		}
	})
	if err := s.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "exited" || in.Exit == nil || in.Exit.Code != 3 {
		t.Fatalf("job = %#v, %v; want exited with code 3", in, err)
	}
}

// D-190/D-210: a child that outlives a stopped job can hold its output open
// (an ssh connection master does). The supervisor must still record the exit
// instead of waiting for that child to end.
func TestSuperviseRecordsStopDespiteChildHoldingOutput(t *testing.T) {
	ready := make(chan time.Time)
	close(ready)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells"), afterStop: func(time.Duration) <-chan time.Time { return ready }}
	fifo := filepath.Join(t.TempDir(), "pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// The holder starts its own session, so the stop's group signal misses it.
	meta, err := s.Create(StartOptions{Cmd: []string{"sh", "-c", "setsid sh -c 'echo $$ > " + fifo + "; exec sleep 600' & wait"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var holder int
	s.OnStarted = func(int) {
		raw, err := os.ReadFile(fifo) // blocks until the command writes the holder's PID
		if err != nil {
			t.Error(err)
		} else if holder, err = strconv.Atoi(strings.TrimSpace(string(raw))); err != nil {
			t.Error(err)
		}
		cancel()
	}
	t.Cleanup(func() {
		if holder > 0 {
			_ = syscall.Kill(holder, syscall.SIGKILL)
		}
	})
	if err := s.Supervise(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "exited" || in.Exit == nil || in.Exit.Signal == "" {
		t.Fatalf("job = %#v, %v; want exited by a signal", in, err)
	}
	if holder == 0 {
		t.Fatal("the helper never reported the holder's PID")
	}
	if err := exec.Command("kill", "-0", strconv.Itoa(holder)).Run(); err != nil {
		t.Fatalf("the holder should still run: %v", err)
	}
}

// An ordinary stop signals the command's group and never cancels the
// supervisor's context. It must be bounded the same way.
func TestStopRecordsExitDespiteChildHoldingOutput(t *testing.T) {
	ready := make(chan time.Time)
	close(ready)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells"), afterStop: func(time.Duration) <-chan time.Time { return ready }}
	fifo := filepath.Join(t.TempDir(), "pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := s.Create(StartOptions{Cmd: []string{"sh", "-c", "setsid sh -c 'echo $$ > " + fifo + "; exec sleep 600' & wait"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var holder int
	started := make(chan struct{})
	s.OnStarted = func(int) {
		raw, err := os.ReadFile(fifo) // blocks until the command writes the holder's PID
		if err != nil {
			t.Error(err)
		} else if holder, err = strconv.Atoi(strings.TrimSpace(string(raw))); err != nil {
			t.Error(err)
		}
		close(started)
	}
	t.Cleanup(func() {
		if holder > 0 {
			_ = syscall.Kill(holder, syscall.SIGKILL)
		}
	})
	done := make(chan error, 1)
	go func() { done <- s.Supervise(context.Background(), meta.ID) }()
	<-started
	in, err := s.Stop(meta.ID, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if in.Status != "exited" || in.Exit == nil || in.Exit.Signal == "" {
		t.Fatalf("stopped job = %#v; want exited by a signal", in)
	}
}
