//go:build linux

package computers

import (
	"context"
	"dyna-agent/internal/shellquote"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSlowStartDoesNotBlockOtherSessionStatus(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runtime, err := os.MkdirTemp("", "ds-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtime)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	bin := t.TempDir()
	marker := filepath.Join(bin, "started")
	gate := filepath.Join(bin, "release")
	script := "#!/bin/sh\nprintf x >> " + shellquote.Quote(marker) + "\nwhile [ ! -e " + shellquote.Quote(gate) + " ]; do /bin/sleep 0.02; done\nexit 1\n"
	for name, body := range map[string]string{"labwc": script, "Xwayland": "#!/bin/sh\nexit 1\n", "setpriv": "#!/bin/sh\nexit 1\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slowID, otherID := testSessionID(t, "slow-start"), testSessionID(t, "other")
	slow, err := newComputer(ctx, slowID, "room", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.cleanup(false)
	other, err := newComputer(ctx, otherID, "room", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.cleanup(false)
	d := &daemon{ctx: ctx, computers: map[string]*supervisor{slowID: slow, otherID: other}}
	started := make(chan error, 1)
	var joined chan error
	go func() { started <- slow.start() }()
	defer func() {
		cancel()
		<-started
		if joined != nil {
			<-joined
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake labwc did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	joined = make(chan error, 1)
	go func() { joined <- slow.start() }()
	select {
	case err := <-joined:
		joined = nil
		t.Fatalf("concurrent connection did not wait: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "x" {
		t.Fatalf("started more than one compositor: %q, %v", b, err)
	}
	done := make(chan error, 1)
	go func() {
		for _, request := range []Request{{Op: "list"}, {Op: "status", Session: otherID}, {Op: "status", Session: slowID}} {
			result, err := d.dispatch(ctx, request)
			if err != nil {
				done <- err
				return
			}
			if request.Session == slowID && result.(Status).State != StateStarting {
				t.Errorf("startup state: %+v", result)
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow startup blocked list or status")
	}
	// Stop must cancel startup without waiting under the state lock.
	if _, err := slow.dispatch(ctx, Request{Op: "stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestStatusPIDsUsesComputerCgroup(t *testing.T) {
	dir := t.TempDir()
	pid := os.Getpid()
	info, ok := processInfo(pid)
	if !ok {
		t.Fatal("cannot read own process")
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{cgroup: dir, apps: map[int]string{1: "unrelated"}}
	got := s.statusPIDs()
	if len(got) != 1 || got[pid] != info.start {
		t.Fatalf("wrong cgroup processes: %v", got)
	}
}

func TestStatusPIDsRejectsReusedTrackedPID(t *testing.T) {
	s := &supervisor{tracked: map[int]string{os.Getpid(): "wrong-start-time"}}
	if got := s.statusPIDs(); len(got) != 0 {
		t.Fatalf("counted reused PID: %v", got)
	}
}
