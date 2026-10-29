package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/shellquote"
)

func TestEmitDoesNotWaitForQueuedSnapshot(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("queued", PRMerged))
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("payload.txt filter=hold\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "payload.txt"), []byte("captured commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-m", "payload")
	source := filepath.Join(t.TempDir(), "emitter")
	git(t, repo, "worktree", "add", "--detach", source, "HEAD")
	sha := git(t, source, "rev-parse", "HEAD")
	pipes := t.TempDir()
	startedPath, releasePath := filepath.Join(pipes, "started"), filepath.Join(pipes, "release")
	for _, path := range []string{startedPath, releasePath} {
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	startedPipe, err := os.OpenFile(startedPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer startedPipe.Close()
	releasePipe, err := os.OpenFile(releasePath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePipe.Close()
	var once sync.Once
	release := func() { once.Do(func() { _, _ = releasePipe.Write([]byte("continue\n")) }) }
	defer release()
	git(t, repo, "config", "filter.hold.smudge", "printf x > "+shellquote.Quote(startedPath)+"; read -r gate < "+shellquote.Quote(releasePath)+"; cat")
	git(t, repo, "config", "filter.hold.required", "true")
	git(t, repo, "config", "filter.hold.clean", "cat")
	started := make(chan error, 1)
	go func() { var signal [1]byte; _, err := startedPipe.Read(signal[:]); started <- err }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	t.Cleanup(func() {
		for _, queued := range queuedEvents(t, s) {
			if queued.Snapshot {
				cleanupSnapshot(queued)
			}
		}
	})
	emitted := make(chan error, 1)
	go func() { emitted <- s.Emit(ctx, Event{Repo: source, Worktree: source, Event: PRMerged}) }()
	select {
	case err := <-emitted:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-started:
		release()
		emitErr := <-emitted
		t.Fatalf("Enqueue waited for snapshot checkout: filter=%v, emit=%v", err, emitErr)
	}
	queued := queuedEvents(t, s)
	if len(queued) != 1 || queued[0].SHA != sha || queued[0].Snapshot {
		t.Fatalf("queued event: %+v", queued)
	}
	git(t, repo, "worktree", "remove", "--force", source)
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "advance main")
	executed := ""
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		executed = p.Dir
		got, err := repoid.Git(ctx, p.Dir, "rev-parse", "HEAD")
		if err != nil || got != sha {
			return fmt.Errorf("snapshot commit = %s, want %s: %v", got, sha, err)
		}
		return nil
	}
	drained := make(chan error, 1)
	go func() { _, _, err := s.Drain(ctx, time.Now()); drained <- err }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-drained:
		t.Fatalf("drain returned before snapshot checkout: %v", err)
	}
	release()
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if executed == "" || executed == source || executed == repo {
		t.Fatalf("hook did not use a private snapshot: %q", executed)
	}
	if _, err := os.Stat(executed); !os.IsNotExist(err) {
		t.Fatalf("snapshot was not cleaned: %v", err)
	}
}

func TestDrainRecordsUnavailableSnapshot(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "unavailable"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			s, repo := setup(t)
			add(t, s, repo, hook("queued", BeforeLand))
			sha := strings.Repeat("0", 40)
			if err := s.Emit(t.Context(), Event{Repo: repo, Event: BeforeLand, SHA: sha}); err != nil {
				t.Fatal(err)
			}
			want := sha
			if missing {
				event := queuedEvents(t, s)[0]
				event.SHA = ""
				setQueued(t, s, event)
				want = "commit SHA"
			}
			called := false
			s.Exec = func(context.Context, processguard.Process) error { called = true; return nil }
			ran, _, err := s.Drain(t.Context(), time.Now())
			if err == nil || !strings.Contains(err.Error(), want) || called {
				t.Fatalf("invalid snapshot ran without its preparation error: called=%v, err=%v", called, err)
			}
			if len(ran) != 1 || len(queuedEvents(t, s)) != 0 {
				t.Fatalf("failed snapshot was not consumed: %v", ran)
			}
			runs, err := s.Runs(t.Context(), repo, "queued")
			if err != nil || len(runs) != 1 || runs[0].Result != "error" {
				t.Fatalf("failed snapshot record: %+v, %v", runs, err)
			}
			raw, err := os.ReadFile(runs[0].LogPath)
			if err != nil || !strings.Contains(string(raw), want) {
				t.Fatalf("snapshot failure log: %s, %v", raw, err)
			}
		})
	}
}
