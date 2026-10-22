package subagent

import (
	"context"
	"dyna-agent/internal/computers"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCloseReleasesComputerThroughTypedProtocol(t *testing.T) {
	e := newTestEnv(t)
	runtime, err := os.MkdirTemp("/tmp", "dcl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtime)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	info, _ := e.start("SLEEP:1 RESPOND: ready", func(o *StartOptions) { o.Name = "computer-owner" })
	e.wait(info.ID, 1)
	os.MkdirAll(computers.Dir(info.ID), 0700)
	os.WriteFile(filepath.Join(computers.Dir(info.ID), "meta.json"), []byte(fmt.Sprintf(`{"session_id":%q}`, info.ID)), 0600)
	socket := computers.ControlSocket(info.ID)
	os.MkdirAll(filepath.Dir(socket), 0700)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	fake := computers.NewFakeSupervisor(info.ID)
	fake.Handle = func(_ context.Context, r computers.Request) (any, error) {
		if r.Op != "destroy" || r.Session != info.ID {
			return nil, fmt.Errorf("wrong cleanup request: %+v", r)
		}
		calls.Add(1)
		return computers.Stopped{State: computers.StateDestroyed}, nil
	}
	go fake.Serve(ctx, listener)
	closed, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true})
	if err != nil || closed.Status != StatusClosed || calls.Load() != 1 {
		t.Fatalf("close: %+v %v cleanup calls=%d", closed, err, calls.Load())
	}
}

func TestCloseContinuesAfterStaleComputerSocket(t *testing.T) {
	e := newTestEnv(t)
	runtime, err := os.MkdirTemp("/tmp", "dcs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtime)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	info, _ := e.start("SLEEP:1 RESPOND: ready", func(o *StartOptions) { o.Name = "stale-computer" })
	e.wait(info.ID, 1)
	os.MkdirAll(computers.Dir(info.ID), 0700)
	os.WriteFile(filepath.Join(computers.Dir(info.ID), "meta.json"), []byte(fmt.Sprintf(`{"session_id":%q}`, info.ID)), 0600)
	socket := computers.ControlSocket(info.ID)
	os.MkdirAll(filepath.Dir(socket), 0700)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
	closed, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true})
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("close: %+v %v", closed, err)
	}
	b, err := os.ReadFile(filepath.Join(computers.Dir(info.ID), "state.json"))
	if err != nil || !strings.Contains(string(b), "destroyed") {
		t.Fatalf("offline cleanup: %s %v", b, err)
	}
	dir, _ := e.m.Store.Dir(info.ID)
	events, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if !strings.Contains(string(events), "session_closed") {
		t.Fatalf("session close transaction did not finish: %s", events)
	}
}
