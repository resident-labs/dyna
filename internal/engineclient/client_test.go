package engineclient

import (
	"context"
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
	// The socket follows the runtime folder, so the CLI and the engine meet on
	// the same path whatever data folder each of them names.
	if got, err := SocketPath(); err != nil || got != filepath.Join(root, "dyna", "engine.sock") {
		t.Fatalf("path = %q, %v", got, err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "relative")
	if _, err := SocketPath(); err == nil {
		t.Fatal("accepted a relative runtime directory")
	}
	if err := noWait("").Call(t.Context(), "Health", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("invalid runtime: %v", err)
	}

	t.Setenv("XDG_RUNTIME_DIR", root)
	socket := filepath.Join(root, "dyna", "engine.sock")
	err := noWait("").Call(t.Context(), "Health", nil, nil)
	if !errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), socket) || strings.Contains(err.Error(), "systemctl") {
		t.Fatalf("missing socket error = %v; want %q without a start command", err, socket)
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
