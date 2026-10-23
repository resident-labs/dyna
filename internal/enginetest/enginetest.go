// Package enginetest serves engine calls on a temporary socket, so CLI and
// worker tests run commands that call the engine without a running service.
package enginetest

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/testsafe"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

// Serve serves calls through the same handler as the engine's Unix socket, at
// engineclient.SocketPath() under a temporary XDG_RUNTIME_DIR, so a command
// that uses engineclient.New("") reaches it. The listener is a plain one, so
// the engine's socket permissions are not under test. Pass fake calls
// (websvc.Bind of test methods) for a fake engine, or the engine's own call
// table for the real one. dataDir is the explicit test data folder; the fake
// runs the same ordered store migrations as engine start before serving.
// It returns the socket path; the server stops when
// the test ends. It sets XDG_RUNTIME_DIR, so the test cannot call t.Parallel.
func Serve(t testing.TB, dataDir string, calls map[string]websvc.Call) string {
	t.Helper()
	return ServeAs(t, dataDir, callspec.Build, calls)
}

// ServeAs is Serve for an engine of the given build.
func ServeAs(t testing.TB, dataDir, build string, calls map[string]websvc.Call) string {
	t.Helper()
	// The fake engine serves its own test process as the owner.
	owner := func(net.Conn) (websvc.Caller, error) { return websvc.Caller{Kind: websvc.Owner}, nil }
	return serve(t, dataDir, build, owner, calls)
}

// ServeCallers is Serve with each connection's caller from callers, called
// when it connects, as the engine identifies a real caller. A test that
// poses as a session, or that runs a real turn runner, can then meet the
// engine's caller checks.
func ServeCallers(t testing.TB, dataDir string, callers func(net.Conn) (websvc.Caller, error), calls map[string]websvc.Call) string {
	t.Helper()
	return serve(t, dataDir, callspec.Build, callers, calls)
}

func serve(t testing.TB, dataDir, build string, callers func(net.Conn) (websvc.Caller, error), calls map[string]websvc.Call) string {
	t.Helper()
	if !filepath.IsAbs(dataDir) {
		t.Fatal("fake engine requires an absolute test data folder")
	}
	// A data folder outside the temporary folder is a real one, such as the
	// CI user's own dyna.db: the fake engine would migrate it.
	if err := testsafe.Check(dataDir); err != nil {
		t.Fatalf("fake engine: %v", err)
	}
	// Store migrations also read the config template registry. Keep it private.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := stores.Migrate(dataDir); err != nil {
		t.Fatalf("start fake engine: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(dataDir); err != nil {
			t.Errorf("close fake engine stores: %v", err)
		}
	})
	// A Unix socket path is limited to about 100 bytes; t.TempDir can be
	// longer, and so can TMPDIR, which storetest.Isolate points at one. So
	// the runtime folder goes straight under /tmp when it exists.
	base := ""
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		base = "/tmp"
	}
	runtime, err := os.MkdirTemp(base, "dyna-engine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	path, err := engineclient.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := devicestore.OpenStore(filepath.Join(dataDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := websvc.New(websvc.Options{Store: store, Routes: websvc.LoopbackRoutes("7457", "7458"),
		Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}}, Calls: calls,
		Hub: hub.New(1), Files: http.NotFoundHandler(), Cards: http.NotFoundHandler(), Version: build,
		Callers: callers})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: srv.UnixHandler(), ConnContext: srv.UnixConnContext}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return path
}
