package terminalsvc

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/store/storetest"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func terminalTest(t *testing.T, o Options) (*Service, Info) {
	t.Helper()
	storetest.DataDir(t)
	t.Cleanup(authority.InEngine())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DYNA_SESSION", "must-not-enter-owner-shell")
	o.Shell = "/bin/sh"
	s := New(o)
	t.Cleanup(s.Shutdown)
	in, err := s.Open("room_test", "", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	return s, in
}
func until(t *testing.T, s *Service, id string, after uint64, match func(Event) bool) Event {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ev, wake, err := s.Read(id, after)
		if err != nil {
			t.Fatal(err)
		}
		if match(ev) {
			return ev
		}
		select {
		case <-wake:
		case <-deadline.C:
			t.Fatalf("output timeout: %q, exited=%v", ev.Data, ev.Exited)
		}
	}
}
func write(t *testing.T, s *Service, id, text string) {
	t.Helper()
	if err := s.Write(id, []byte(text)); err != nil {
		t.Fatal(err)
	}
}

func TestLoginCommandPreservesArgumentsOverridesEnvAndExits(t *testing.T) {
	t.Setenv("DYNA_SESSION", "agent-stamp")
	t.Setenv("LOGIN_TEST", "inherited")
	s := New(Options{})
	t.Cleanup(s.Shutdown)
	argument := "literal ; $(never-run)"
	info, err := s.OpenCommand([]string{"/bin/sh", "-c", `printf '%s|%s|%s' "$1" "$LOGIN_TEST" "${DYNA_SESSION-unset}"; exit 7`, "login", argument}, []string{"LOGIN_TEST=override", "DYNA_SESSION=also-removed"}, t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ev := until(t, s, info.ID, 0, func(ev Event) bool { return ev.Exited })
	if ev.ExitCode != 7 || string(ev.Data) != argument+"|override|unset" {
		t.Fatalf("login argv/env/exit mismatch: code=%d, output=%q", ev.ExitCode, ev.Data)
	}
	list := s.List("")
	if len(list) != 1 || !list[0].Exited || list[0].Alive {
		t.Fatal("login command left a live shell")
	}
}

func TestShellRoundTripResizeReattachClose(t *testing.T) {
	s, in := terminalTest(t, Options{})
	if !in.Alive || in.Ref != "room_test" || len(s.List("room_test")) != 1 {
		t.Fatalf("open: %+v", in)
	}
	write(t, s, in.ID, "printf '%s%s\\n' round- trip\n")
	first := until(t, s, in.ID, 0, func(ev Event) bool { return strings.Contains(string(ev.Data), "round-trip") })
	if err := s.Resize(in.ID, 103, 41); err != nil {
		t.Fatal(err)
	}
	write(t, s, in.ID, "stty size\n")
	size := until(t, s, in.ID, first.Offset, func(ev Event) bool { return strings.Contains(string(ev.Data), "41 103") })
	if list := s.List(""); list[0].Cols != 103 || list[0].Rows != 41 {
		t.Fatalf("size: %+v", list)
	}
	// Disconnecting an output reader never ends the shell.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Stream(ctx, in.ID, size.Offset, func(Event) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("detach: %v", err)
	}
	write(t, s, in.ID, "printf '%s%s\\n' after- detach\n")
	until(t, s, in.ID, size.Offset, func(ev Event) bool { return strings.Contains(string(ev.Data), "after-detach") })
	replay, _, err := s.Read(in.ID, 0)
	if err != nil || !strings.Contains(string(replay.Data), "round-trip") || !strings.Contains(string(replay.Data), "after-detach") {
		t.Fatalf("reattach: %+v %v", replay, err)
	}
	write(t, s, in.ID, "printf '<%s%s>\\n' identity- \"${DYNA_SESSION-unset}\"\n")
	until(t, s, in.ID, replay.Offset, func(ev Event) bool { return strings.Contains(string(ev.Data), "<identity-unset>") })
	term, _ := s.get(in.ID)
	write(t, s, in.ID, "sleep 30\n")
	if err := s.Close(in.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.done:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not stop the shell")
	}
	if len(s.List("")) != 0 {
		t.Fatal("closed terminal stayed listed")
	}
	if _, _, err := s.Read(in.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed read: %v", err)
	}
}

func TestScrollbackCapAndNaturalExit(t *testing.T) {
	s, in := terminalTest(t, Options{})
	write(t, s, in.ID, "printf '%0300000d' 0; printf '%s%s\\n' buffer- done; exit 7\n")
	ev := until(t, s, in.ID, 0, func(ev Event) bool { return ev.Exited })
	if len(ev.Data) != ScrollbackBytes || !ev.Reset || ev.Start == 0 || !strings.Contains(string(ev.Data), "buffer-done") || ev.ExitCode != 7 {
		t.Fatalf("buffer=%d start=%d reset=%v exit=%d", len(ev.Data), ev.Start, ev.Reset, ev.ExitCode)
	}
	if err := s.Write(in.ID, []byte("echo nope\n")); err == nil {
		t.Fatal("write to exited shell succeeded")
	}
	if s.List("")[0].Alive {
		t.Fatal("exited terminal is alive")
	}
}
func TestIdleReap(t *testing.T) {
	s, in := terminalTest(t, Options{IdleTimeout: 60 * time.Millisecond})
	term, _ := s.get(in.ID)
	select {
	case <-term.done:
	case <-time.After(3 * time.Second):
		t.Fatal("idle shell was not reaped")
	}
	if len(s.List("")) != 0 {
		t.Fatal("reaped shell stayed listed")
	}
}

func TestOpenLimits(t *testing.T) {
	// Idle reaping is off so a loaded host cannot free the slot mid-test.
	s, _ := terminalTest(t, Options{IdleTimeout: -1, MaxTerminals: 1})
	if _, err := s.Open("room_test", "", t.TempDir(), 80, 24); err == nil {
		t.Fatal("terminal cap ignored")
	}
	s, _ = terminalTest(t, Options{IdleTimeout: -1})
	for _, size := range [][2]uint16{{0, 24}, {80, 0}, {1001, 24}} {
		if _, err := s.Open("room_test", "", t.TempDir(), size[0], size[1]); err == nil {
			t.Fatal("bad size accepted")
		}
	}
}

func TestCloseStopsBackgroundJob(t *testing.T) {
	s, in := terminalTest(t, Options{})
	write(t, s, in.ID, "sleep 30 & printf '__bg_pid_%d__\\n' $!; wait\n")
	pattern := regexp.MustCompile(`__bg_pid_([0-9]+)__`)
	ev := until(t, s, in.ID, 0, func(ev Event) bool { return pattern.Match(ev.Data) })
	pid := pattern.FindSubmatch(ev.Data)[1]
	term, err := s.get(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(in.ID); err != nil {
		t.Fatal(err)
	}
	if term.cmd.ProcessState == nil || !term.cmd.ProcessState.Sys().(syscall.WaitStatus).Signaled() {
		t.Fatal("close did not reap the killed shell")
	}
	// Close reaps its shell. A background child can remain in kernel I/O
	// with SIGKILL pending until that I/O completes.
	var status []byte
	for deadline := time.Now().Add(3 * time.Second); ; {
		b, err := os.ReadFile("/proc/" + string(pid) + "/status")
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		status = b
		if backgroundJobTerminating(b) {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("background job has no termination request after close: %s", status)
}

func backgroundJobTerminating(status []byte) bool {
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "State:" && (fields[1] == "Z" || fields[1] == "X") {
			return true
		}
		if fields[0] == "SigPnd:" || fields[0] == "ShdPnd:" {
			pending, err := strconv.ParseUint(fields[1], 16, 64)
			if err == nil && pending&(1<<(syscall.SIGKILL-1)) != 0 {
				return true
			}
		}
	}
	return false
}

func TestBackgroundJobTerminationStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   bool
	}{
		{"zombie", "State:\tZ (zombie)\n", true},
		{"dead", "State:\tX (dead)\n", true},
		{"disk_wait_with_thread_kill", "State:\tD (disk sleep)\nSigPnd:\t0000000000000100\nShdPnd:\t0000000000000000\n", true},
		{"disk_wait_with_shared_kill", "State:\tD (disk sleep)\nSigPnd:\t0000000000000000\nShdPnd:\t0000000000000100\n", true},
		{"disk_wait_without_kill", "State:\tD (disk sleep)\nSigPnd:\t0000000000000000\nShdPnd:\t0000000000000000\n", false},
		{"other_signal", "State:\tD (disk sleep)\nShdPnd:\t0000000000004000\n", false},
		{"running", "State:\tR (running)\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backgroundJobTerminating([]byte(tc.status)); got != tc.want {
				t.Fatalf("termination requested = %v, want %v; status: %s", got, tc.want, tc.status)
			}
		})
	}
}
