//go:build linux

package computers

import (
	"context"
	"errors"
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
