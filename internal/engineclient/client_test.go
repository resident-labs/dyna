package engineclient

import (
	"context"
	"dyna-agent/internal/enginesocket"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noWait is a client that does not wait for a down engine to start.
func noWait(path string) *Client { return New(path, RestartWait(0)) }

func TestEngineDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	for _, stale := range []bool{false, true} {
		if stale {
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
		}
		err := noWait(path).Call(t.Context(), "AccountsList", []any{"", false}, nil)
		if !errors.Is(err, ErrNotRunning) {
			t.Fatalf("stale=%v: got %v", stale, err)
		}
	}
}

func TestSocketPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", root)
	// The owner's own data folder keeps the historical path, so an installed
	// engine and the CLI still meet. The owner's data folder is the standard one
	// under HOME, as an installed engine has it.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	want, err := enginesocket.PathFor(root, enginesocket.OwnerDataDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := SocketPath(); err != nil || got != want {
		t.Fatalf("owner path = %q, %v; want %q", got, err, want)
	}
	// Any other data folder gets one of its own, so a second engine cannot take
	// the owner's socket.
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "other-data"))
	other, err := SocketPath()
	if err != nil || other == want || !strings.Contains(other, "engines") {
		t.Fatalf("other data folder path = %q, %v; want a socket of its own", other, err)
	}
	for _, invalid := range []string{"", "relative"} {
		t.Setenv("XDG_RUNTIME_DIR", invalid)
		if _, err := SocketPath(); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
		if err := noWait("").Call(t.Context(), "Health", nil, nil); !errors.Is(err, ErrNotRunning) {
			t.Fatalf("missing runtime: %v", err)
		}
	}
}

func TestCancelledCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	cancel()
	err := New(filepath.Join(t.TempDir(), "engine.sock")).Call(ctx, "Health", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestRunningDialsTheSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	if Running(path) {
		t.Fatal("Running reported an engine with no socket")
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if !Running(path) {
		t.Fatal("Running missed a listening engine")
	}
	ln.Close()
	if Running(path) {
		t.Fatal("Running reported a closed engine socket")
	}
}
