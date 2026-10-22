package computers

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startupEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func startFakeComputerService(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ControlSocket("")), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", ControlSocket(""))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewFakeSupervisor("survivor").Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

func TestEnsureReusesServiceAfterCallerStops(t *testing.T) {
	startupEnvironment(t)
	startFakeComputerService(t)
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	firstCaller, stopFirstCaller := context.WithCancel(context.Background())
	defer stopFirstCaller()
	if _, err := Ensure(firstCaller, "survivor", "room"); err != nil {
		t.Fatal(err)
	}
	stopFirstCaller()
	secondCaller, stopSecondCaller := context.WithTimeout(context.Background(), time.Second)
	defer stopSecondCaller()
	if _, err := Ensure(secondCaller, "survivor", "room"); err != nil {
		t.Fatal(err)
	}
	status, err := NewClient("survivor").Status(secondCaller)
	if err != nil || status.SessionID != "survivor" {
		t.Fatalf("existing computer did not survive caller cancellation: %+v, %v", status, err)
	}
}

func TestEnsureCancelledCallerDoesNotStartDaemon(t *testing.T) {
	startupEnvironment(t)
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Ensure(ctx, "cancelled", ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(Root()); !os.IsNotExist(err) {
		t.Fatal("cancelled caller created daemon state")
	}
}
