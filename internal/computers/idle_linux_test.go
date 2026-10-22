//go:build linux

package computers

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
	"dyna-agent/internal/turnlock"
)

// An open app holds a relay connection for its whole life. It must not keep
// an unwatched computer alive; only a running turn or shell job of its session
// does, and a lock left with a departed runner does not.
func TestIdleStopIgnoresOpenAppsButWaitsForTheSessionTurn(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	id := testSessionID(t, "idle")
	if err := os.MkdirAll(Dir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := newComputer(t.Context(), id, "test", SupervisorOptions{IdleAfter: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cleanup(false) })
	desktop, err := startProcess(os.Environ(), filepath.Join(t.TempDir(), "desktop.log"), "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.DataDir(), "sessions", id, "turn.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	gate := func(pid int) {
		t.Helper()
		b := []byte(fmt.Sprintf(`{"active":true,"pid":%d,"turn":1}`, pid))
		if err := os.WriteFile(filepath.Join(filepath.Dir(lockPath), turnlock.GateFile), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gate(os.Getpid()) // a live runner

	s.mu.Lock()
	defer s.mu.Unlock()
	s.compositor = desktop
	s.relayConns[&net.UnixConn{}] = true // the open browser's Wayland connection
	s.lastActivity = time.Now().Add(-time.Hour)
	s.stopIfIdle()
	if !s.compositor.alive() || time.Since(s.lastActivity) > time.Minute {
		t.Fatalf("a computer stopped while its session's turn ran: state %s", s.state.State)
	}
	_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)

	// A running shell job of the session, such as a QA script, also counts.
	jobs := shells.DefaultStore()
	job, err := jobs.Create(shells.StartOptions{Owner: id, Name: "qa", Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	job.Supervisor = os.Getpid() // a live supervisor makes the job "running"
	if err := jobs.Save(job, nil); err != nil {
		t.Fatal(err)
	}
	s.lastActivity = time.Now().Add(-time.Hour)
	s.stopIfIdle()
	if !s.compositor.alive() {
		t.Fatal("a computer stopped while its session's shell job ran")
	}
	if err := jobs.Save(job, &shells.Exit{Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// A descendant of an older runner can keep turn.lock after the runner
	// exits. A held lock whose gate runner is gone must not keep the computer.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	gate(gone.Process.Pid)
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	s.lastActivity = time.Now().Add(-time.Hour)
	s.stopIfIdle()
	if s.compositor.alive() || s.state.State != StateIdleStopped {
		t.Fatalf("an idle computer with an open app kept running: state %s", s.state.State)
	}
}
