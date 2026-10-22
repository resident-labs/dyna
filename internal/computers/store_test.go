package computers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentFailureWritesNeverCorrupt covers the JSON file writer that
// every computer document shares: concurrent writers to one path must never
// leave a corrupt or partial file, and the file keeps its mode and format.
func TestConcurrentFailureWritesNeverCorrupt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := "concurrent-writers"
	path := filepath.Join(Dir(id), "failure.json")
	stop := make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				observed <- nil
				return
			default:
			}
			b, err := os.ReadFile(path)
			if err != nil || len(b) == 0 {
				continue
			}
			var v map[string]string
			if jerr := json.Unmarshal(b, &v); jerr != nil || v["reason"] == "" {
				observed <- fmt.Errorf("corrupt or partial failure.json (%d bytes): %v\n%s", len(b), jerr, preview(b))
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 150; i++ {
				unit := fmt.Sprintf("worker-%d-write-%d ", w, i)
				RecordFailure(id, errors.New(strings.Repeat(unit, 5+(w*150+i)%250)))
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("failure.json is corrupt: %v\n%s", err, preview(b))
	}
	if len(v) != 2 || v["reason"] == "" || v["at"] == "" {
		t.Fatalf("failure.json changed format: %s", preview(b))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("failure.json mode %v, want 0600", info.Mode().Perm())
	}
}

func preview(b []byte) string {
	if len(b) > 240 {
		return string(b[:240]) + "..."
	}
	return string(b)
}

func TestEnvironmentPreservesOwnerServices(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := []string{"PATH=/usr/bin:/bin", "WAYLAND_DISPLAY=wayland-owner", "DISPLAY=:0", "DBUS_SESSION_BUS_ADDRESS=unix:path=/owner/bus", "XDG_RUNTIME_DIR=/owner/runtime", "HYPRLAND_INSTANCE_SIGNATURE=owner-hypr"}
	m := Meta{SessionID: "session", WaylandDisplay: "dyna-public", Display: ":234"}
	env, err := Environment(m, base)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"WAYLAND_DISPLAY": "dyna-public", "DISPLAY": ":234", "DYNA_OWNER_WAYLAND_DISPLAY": "wayland-owner", "DYNA_OWNER_DISPLAY": ":0", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/owner/bus", "XDG_RUNTIME_DIR": "/owner/runtime", "HYPRLAND_INSTANCE_SIGNATURE": "owner-hypr"} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	twice, err := Environment(m, env)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(twice, "DYNA_OWNER_WAYLAND_DISPLAY") != "wayland-owner" {
		t.Fatal("lost owner display")
	}
	if envValue(env, "DYNA_COMPUTER") != "" {
		t.Fatal("injected dropped variable")
	}
}
func TestDisabledInjectionStartsNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dyna")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"version":1,"computers":{"enabled":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"DYNA_SESSION=session", "WAYLAND_DISPLAY=owner"}
	got, err := Inject(context.Background(), base)
	if err != nil || strings.Join(got, "\n") != strings.Join(base, "\n") {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(Root()); !os.IsNotExist(err) {
		t.Fatal("created computer data")
	}
}

func TestInjectRegistersStampedSessionWithFake(t *testing.T) {
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime, err := os.MkdirTemp("/tmp", "dci-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtime)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700)
	t.Setenv("PATH", bin)
	socket := ControlSocket("")
	os.MkdirAll(filepath.Dir(socket), 0700)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := NewFakeSupervisor("stamped")
	fake.Handle = func(_ context.Context, r Request) (any, error) {
		if r.Op != "register" || r.Session != "stamped" || r.Room != "room-id" {
			return nil, fmt.Errorf("wrong registration: %+v", r)
		}
		return Meta{SessionID: r.Session, Room: r.Room, WaylandDisplay: "public-test", Display: ":345"}, nil
	}
	go fake.Serve(ctx, listener)
	base := []string{"DYNA_SESSION=stamped", "DYNA_ROOM=room", "DYNA_ROOM_ID=room-id", "WAYLAND_DISPLAY=owner", "DISPLAY=:0", "PATH=/usr/bin", "DBUS_SESSION_BUS_ADDRESS=owner-bus"}
	got, err := Inject(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(got, "WAYLAND_DISPLAY") != "public-test" || envValue(got, "DISPLAY") != ":345" || envValue(got, "DBUS_SESSION_BUS_ADDRESS") != "owner-bus" {
		t.Fatal(got)
	}
}

func TestHypruseShimUsesOwnerDisplays(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "hypruse"), []byte("#!/bin/sh\nprintf '%s|%s|%s' \"$WAYLAND_DISPLAY\" \"$DISPLAY\" \"$*\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	first, err := browserShims("first", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := browserShims("second", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(first, "hypruse")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DYNA_OWNER_WAYLAND_DISPLAY", "DYNA_OWNER_DISPLAY", "export PATH", "exec hypruse"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("shim lacks %q: %s", want, body)
		}
	}
	for _, test := range []struct{ ownerWayland, ownerDisplay, want string }{
		{"owner-wayland", ":0", "owner-wayland|:0|screenshot"},
		{"", "", "agent-wayland|:234|screenshot"},
		{"owner-wayland", "", "owner-wayland|:234|screenshot"},
	} {
		cmd := exec.Command(path, "screenshot")
		cmd.Env = []string{"PATH=" + strings.Join([]string{first, second, real}, ":"), "WAYLAND_DISPLAY=agent-wayland", "DISPLAY=:234", "DYNA_OWNER_WAYLAND_DISPLAY=" + test.ownerWayland, "DYNA_OWNER_DISPLAY=" + test.ownerDisplay}
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != test.want {
			t.Fatalf("shim returned %q, %v; want %q", out, err, test.want)
		}
	}
}
