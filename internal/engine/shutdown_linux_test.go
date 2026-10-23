//go:build linux

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"golang.org/x/sys/unix"
)

func TestWorkersCancelSlowGitHubWithInheritedOutput(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	scoped, err := room.DefaultStore().Create(room.CreateInput{Name: "shutdown"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, room.DefaultStore(), scoped.ID, "github.com", "example/repo", "read-only")
	root := t.TempDir()
	readyPath, releasePath := filepath.Join(root, "ready"), filepath.Join(root, "release")
	for _, path := range []string{readyPath, releasePath} {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	release, err := os.OpenFile(releasePath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()
	gh := filepath.Join(root, "gh")
	script := "#!/bin/sh\n[ \"$1\" = auth ] && exit 0\ncat '" + releasePath + "' &\nprintf x > '" + readyPath + "'\nwait\n"
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	w := testWorkers(nil)
	w.prs = func(ctx context.Context) (time.Time, error) {
		_, err := (watch.GitHub{Executable: gh}).Snapshot(ctx, watch.Ref{Host: "github.com", Repo: "example/repo", Number: 1})
		return time.Time{}, err
	}
	go func() {
		defer close(done)
		w.run(ctx, newWorkerSignals(nil), newFakeClock().timers(ctx), func(string, error) {})
	}()
	defer func() {
		cancel()
		release.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("worker cleanup did not finish")
		}
	}()
	entered := make(chan error, 1)
	go func() { var b [1]byte; _, err := ready.Read(b[:]); entered <- err }()
	select {
	case err := <-entered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake gh did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("engine workers waited for a gh descendant after cancellation")
	}
}
