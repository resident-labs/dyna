//go:build linux

package computers

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store"
)

// A daemon writes its pid file only after it starts. Destroying the computer
// before that must still stop the daemon, found by its environment.
func TestOfflineDestroyStopsDaemonBeforePidFile(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := testSessionID(t, "early-destroy")
	root := browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id)
	for _, dir := range []string{root, Dir(id)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WriteJSON(filepath.Join(Dir(id), "meta.json"), Meta{SessionID: id}, 0o600); err != nil {
		t.Fatal(err)
	}
	env := mergeEnvironment(os.Environ(), map[string]string{"AGENT_BROWSER_SOCKET_DIR": root, "AGENT_BROWSER_DAEMON": "1"})
	ready := readyFIFO(t)
	// The shell signals after its own exec, so /proc/<pid>/environ is readable
	// before destroy scans for the daemon.
	child, err := startProcess(env, filepath.Join(Dir(id), "daemon.log"), "/bin/sh", "-c", "printf ready > "+shellquote.Quote(ready)+"; exec sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	defer child.stop()
	awaitReady(t, ready)
	if err := destroyOffline(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.done:
	case <-time.After(8 * time.Second):
		t.Fatal("destroy before the pid file left the daemon running")
	}
}

// Right after exec, /proc/<pid>/environ reads as empty. A destroy in that
// window must read the process again instead of skipping it.
func TestOfflineDestroyWaitsForEmptyEnvironment(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := testSessionID(t, "empty-environ")
	root := browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id)
	for _, dir := range []string{root, Dir(id)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WriteJSON(filepath.Join(Dir(id), "meta.json"), Meta{SessionID: id}, 0o600); err != nil {
		t.Fatal(err)
	}
	env := mergeEnvironment(os.Environ(), map[string]string{"AGENT_BROWSER_SOCKET_DIR": root, "AGENT_BROWSER_DAEMON": "1"})
	child, err := startProcess(env, filepath.Join(Dir(id), "daemon.log"), "/bin/sleep", "600")
	if err != nil {
		t.Fatal(err)
	}
	defer child.stop()
	pid := child.cmd.Process.Pid
	reads := 0
	real := readProcessEnviron
	readProcessEnviron = func(p int) ([]byte, error) {
		if p == pid && reads < 4 {
			reads++
			return nil, nil
		}
		return real(p)
	}
	t.Cleanup(func() { readProcessEnviron = real })
	if err := destroyOffline(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.done:
	case <-time.After(8 * time.Second):
		t.Fatal("destroy skipped a daemon whose environment read as empty")
	}
	if reads < 4 {
		t.Fatalf("the empty environment was read %d times, want it read again until readable", reads)
	}
}

// A daemon that accepts the destroy call but never answers must not leave the
// browser running: the caller falls back to the offline path.
func TestDestroyStopsBrowserWhenDaemonDoesNotAnswer(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := testSessionID(t, "hung-destroy")
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(Dir(id), "meta.json"), Meta{SessionID: id}, 0o600); err != nil {
		t.Fatal(err)
	}
	socket := ControlSocket(id)
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	ready := readyFIFO(t)
	// The shell signals after its own exec, so /proc/<pid>/environ is already
	// readable when destroy scans for the daemon.
	browser, err := startProcess(mergeEnvironment(os.Environ(), map[string]string{"AGENT_BROWSER_SOCKET_DIR": root, "AGENT_BROWSER_DAEMON": "1"}), filepath.Join(Dir(id), "browser.log"), "/bin/sh", "-c", "printf ready > "+shellquote.Quote(ready)+"; exec sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	defer browser.stop()
	awaitReady(t, ready)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := Destroy(ctx, id); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	select {
	case <-browser.done:
	case <-time.After(3 * time.Second):
		t.Fatal("destroy left the browser daemon running")
	}
	state := savedStatus(id)
	if state.State != StateDestroyed {
		t.Fatalf("state after destroy: %+v", state)
	}
}
