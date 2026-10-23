package terminalsvc

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/store/storetest"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	pipe := filepath.Join(t.TempDir(), "background-exit")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	// The background child holds this pipe for its whole life. It opens the
	// pipe only after it starts, so a reader proves the child runs before
	// Close, and the pipe's EOF proves the group kill reached it.
	holders := make(chan *os.File, 1)
	go func() {
		f, err := os.OpenFile(pipe, os.O_RDONLY, 0)
		if err != nil {
			holders <- nil
			return
		}
		holders <- f
	}()
	write(t, s, in.ID, fmt.Sprintf("( exec 3>%q; sleep 30 ) & wait\n", pipe))
	var reader *os.File
	select {
	case reader = <-holders:
	case <-time.After(5 * time.Second):
		t.Fatal("the background job never started")
	}
	if reader == nil {
		t.Fatal("the background job never started")
	}
	defer reader.Close()
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
	// Close reaps its shell and kills the rest of the group. The child's own
	// pipe reaches EOF when it exits, so a survivor fails here.
	closed := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); closed <- err }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("reading the background job's exit pipe: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not stop the background job")
	}
}
