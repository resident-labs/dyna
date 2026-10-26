//go:build linux

package computers

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
)

func setRestartPending(t *testing.T, pending bool) {
	t.Helper()
	original := restartPending
	t.Cleanup(func() { restartPending = original })
	restartPending = func(time.Time) bool { return pending }
}

// A deferred restart waits for the busy computers to finish. A new desktop must
// not join them, or the set of busy computers could grow forever.
func TestStartRefusesNewDesktopWhileRestartIsPending(t *testing.T) {
	setRestartPending(t, true)
	s := &supervisor{ctx: context.Background()}
	if err := s.start(); !errors.Is(err, ErrServiceUpdating) {
		t.Fatalf("start = %v, want ErrServiceUpdating", err)
	}
	if s.starting != nil {
		t.Fatal("a refused start left a startup attempt behind")
	}
}

func TestDaemonReportsItsBuild(t *testing.T) {
	original := callspec.Build
	t.Cleanup(func() { callspec.Build = original })
	callspec.Build = "dev+test"
	got, err := (&daemon{}).dispatch(context.Background(), Request{Op: "version"})
	if err != nil || got != "dev+test" {
		t.Fatalf("version = %v, %v", got, err)
	}
}

// A stopped computer whose desktop the service will refuse must make open fail
// with that reason, and must not launch the app: the app would connect, be
// closed, and exit with "connection reset by peer" (D-280).
func TestOpenOfAStoppedComputerSaysWhyItCannotStartAndLaunchesNothing(t *testing.T) {
	setRestartPending(t, true)
	marker := t.TempDir() + "/launched"
	s := &supervisor{ctx: context.Background()}
	_, err := s.openApp(context.Background(), Request{Op: "open", Status: "Checking a refused open", Command: []string{"sh", "-c", "touch " + marker}, TimeoutMS: 2000})
	if !errors.Is(err, ErrServiceUpdating) {
		t.Fatalf("open = %v, want ErrServiceUpdating", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the app was launched although its desktop cannot start")
	}
}
