package hooks

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/store"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func queuedEvents(t *testing.T, s Service) []Event {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.DataDir, "hook-spool"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.DataDir, "hook-spool", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}

func TestEmitSpoolsWithoutStartingAProcess(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("queued", AfterLand))
	commands := 0
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		commands++
		return nil
	}
	// The test executable is not a Dyna CLI. Emit must only write the spool.
	if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand, Branch: "first"}); err != nil {
		t.Fatal(err)
	}
	events := queuedEvents(t, s)
	if len(events) != 1 || events[0].Branch != "first" || commands != 0 {
		t.Fatalf("queued events: %#v; commands %d", events, commands)
	}
}

func TestSpoolSurvivesRestartAndDispatchesInOrderOnce(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("queued", AfterLand))
	ctx := context.Background()
	for _, branch := range []string{"first", "second"} {
		if err := s.Emit(ctx, Event{Repo: repo, Event: AfterLand, Branch: branch}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		for _, env := range p.Env {
			if strings.HasPrefix(env, "DYNA_HOOK_BRANCH=") {
				got = append(got, strings.TrimPrefix(env, "DYNA_HOOK_BRANCH="))
			}
		}
		// The queued event must still exist while Dispatch is running.
		if len(queuedEvents(t, s)) == 0 {
			t.Error("event removed before dispatch finished")
		}
		return nil
	}
	// A fresh service reads only durable state from the previous emitter.
	restarted := Service{DataDir: s.DataDir, Exec: s.Exec, Notify: s.Notify}
	if _, _, err := restarted.Drain(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.Drain(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "first,second" || len(queuedEvents(t, s)) != 0 {
		t.Fatalf("dispatches: %v", got)
	}
}

func TestSpoolCancellationPreservesCapturedEvent(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "queued commit"
		if legacy {
			name = "legacy prepared snapshot"
		}
		t.Run(name, func(t *testing.T) {
			s, repo := setup(t)
			add(t, s, repo, hook("queued", BeforeLand))
			sha := git(t, repo, "rev-parse", "HEAD")
			if err := s.Emit(t.Context(), Event{Repo: repo, Event: BeforeLand, SHA: sha}); err != nil {
				t.Fatal(err)
			}
			e := queuedEvents(t, s)[0]
			if legacy {
				var err error
				e, err = snapshot(t.Context(), e)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cleanupSnapshot(e) })
				if err := store.WriteJSON(filepath.Join(s.spoolDir(), "00000000000000000001.json"), e, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			executionTree := ""
			s.Exec = func(ctx context.Context, p processguard.Process) error {
				executionTree = p.Dir
				cancel()
				return ctx.Err()
			}
			if _, _, err := s.Drain(ctx, time.Now()); !errors.Is(err, context.Canceled) {
				t.Fatalf("drain: %v", err)
			}
			pending := queuedEvents(t, s)
			if len(pending) != 1 || pending[0].SHA != sha {
				t.Fatalf("cancelled event lost its commit: %+v", pending)
			}
			_, err := os.Stat(executionTree)
			if legacy && err != nil {
				t.Fatalf("legacy snapshot was removed before its event: %v", err)
			}
			if !legacy && !os.IsNotExist(err) {
				t.Fatalf("cancelled worker retained its temporary snapshot: %v", err)
			}
			s.Exec = func(_ context.Context, p processguard.Process) error { executionTree = p.Dir; return nil }
			if _, _, err := s.Drain(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if len(queuedEvents(t, s)) != 0 {
				t.Fatal("completed event retained")
			}
			if _, err := os.Stat(executionTree); !os.IsNotExist(err) {
				t.Fatalf("snapshot retained after dispatch: %v", err)
			}
		})
	}
}

func TestBlockingHookWaitsForCommand(t *testing.T) {
	s, repo := setup(t)
	h := hook("gate", BeforeLand)
	h.OnFail = "block"
	add(t, s, repo, h)
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.Exec = func(ctx context.Context, _ processguard.Process) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.Emit(ctx, Event{Repo: repo, Event: BeforeLand}) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("Emit returned before the command started: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("Emit returned while gate was blocked: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(queuedEvents(t, s)) != 0 {
		t.Fatal("blocking hook was queued")
	}
}

func TestConcurrentSpoolWritersKeepEveryEvent(t *testing.T) {
	s, repo := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Enqueue(Event{Repo: repo, Event: AfterLand}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := len(queuedEvents(t, s)); got != 12 {
		t.Fatalf("queued %d events, want 12", got)
	}
}

func TestSpoolHookFailureIsConsumed(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("fail", AfterLand))
	if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand}); err != nil {
		t.Fatal(err)
	}
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		return errors.New("hook failed")
	}
	if _, _, err := s.Drain(context.Background(), time.Now()); err == nil {
		t.Fatal("hook failure not reported")
	}
	if len(queuedEvents(t, s)) != 0 {
		t.Fatal("completed failed hook was retained")
	}
}

func TestQueuedBeforeLandSurvivesRemovedEmitterTree(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("queued", BeforeLand))
	tree := filepath.Join(t.TempDir(), "emitter")
	git(t, repo, "worktree", "add", "--detach", tree, "HEAD")
	sha := git(t, tree, "rev-parse", "HEAD")
	if err := s.Emit(context.Background(), Event{Repo: tree, Worktree: tree, SHA: sha, Event: BeforeLand}); err != nil {
		t.Fatal(err)
	}
	e := queuedEvents(t, s)[0]
	t.Cleanup(func() {
		if e.Snapshot {
			cleanupSnapshot(e)
		}
	})
	git(t, repo, "worktree", "remove", "--force", tree)
	if e.Repo != repo {
		t.Fatalf("queued transient repo: %s", e.Repo)
	}
	t.Cleanup(authority.InEngine())
	if _, _, err := s.Drain(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(context.Background(), repo, "queued")
	if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
		t.Fatalf("runs: %#v; %v", runs, err)
	}
}

func TestQueuedLifecycleHooksSurviveRemovedExecutionTree(t *testing.T) {
	for _, event := range []string{SessionFinish, PRMerged} {
		for _, cwd := range []string{"worktree", "main"} {
			t.Run(event+"/"+cwd, func(t *testing.T) {
				s, repo := setup(t)
				h := hook("queued", event)
				h.Cwd = cwd
				add(t, s, repo, h)
				tree := filepath.Join(t.TempDir(), "emitter")
				git(t, repo, "worktree", "add", "-b", "finished-turn", tree)
				sha := git(t, tree, "rev-parse", "HEAD")
				if err := s.Emit(context.Background(), Event{Repo: tree, Worktree: tree, Event: event}); err != nil {
					t.Fatal(err)
				}
				queued := queuedEvents(t, s)[0]
				t.Cleanup(func() {
					if queued.Snapshot {
						cleanupSnapshot(queued)
					}
				})
				git(t, repo, "worktree", "remove", "--force", tree)
				t.Cleanup(authority.InEngine())
				if _, _, err := s.Drain(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
				runs, err := s.Runs(context.Background(), repo, h.Name)
				if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
					t.Fatalf("runs: %#v; %v", runs, err)
				}
				if queued.SHA != sha || queued.Branch != "finished-turn" {
					t.Fatalf("event metadata lost: %#v", queued)
				}
			})
		}
	}
}

func TestDrainQuarantinesUnparsableEventAndRunsLaterEvent(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("queued", AfterLand))
	if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand}); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(s.spoolDir(), "00000000000000000001.json")
	if err := os.WriteFile(first, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.InEngine())
	if _, _, err := s.Drain(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(first + ".bad"); err != nil || string(data) != "{broken" {
		t.Fatalf("quarantine: %q; %v", data, err)
	}
	runs, err := s.Runs(context.Background(), repo, "queued")
	if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
		t.Fatalf("runs: %#v; %v", runs, err)
	}
	if len(queuedEvents(t, s)) != 0 {
		t.Fatal("later event was not drained")
	}
}

func TestSpoolQuarantinesNonNumericNames(t *testing.T) {
	for _, operation := range []string{"enqueue", "drain"} {
		t.Run(operation, func(t *testing.T) {
			s, repo := setup(t)
			add(t, s, repo, hook("queued", AfterLand))
			if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand}); err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(s.spoolDir(), "not-a-number.json")
			if err := os.WriteFile(bad, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if operation == "enqueue" {
				if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand}); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(authority.InEngine())
			if _, _, err := s.Drain(context.Background(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(bad + ".bad"); err != nil || string(data) != "{}" {
				t.Fatalf("quarantine: %q; %v", data, err)
			}
			runs, err := s.Runs(context.Background(), repo, "queued")
			want := 1
			if operation == "enqueue" {
				want = 2
			}
			if err != nil || len(runs) != want {
				t.Fatalf("runs: %#v; %v", runs, err)
			}
		})
	}
}

// A drain that another process holds returns when to retry. A drain that
// finishes returns the spool files it ran and no retry.
func TestDrainRetriesOnlyWhenItCannotFinish(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("ok", AfterLand))
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		return nil
	}
	if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand}); err != nil {
		t.Fatal(err)
	}
	unlock, owned, err := store.TryLock(filepath.Join(s.spoolDir(), "drain.lock"))
	if err != nil || !owned {
		t.Fatal(owned, err)
	}
	now := time.Now()
	ran, retry, err := s.Drain(context.Background(), now)
	if err != nil || len(ran) != 0 || !retry.Equal(now.Add(drainRetry)) {
		t.Fatalf("held drain = %v, %v, %v; want a retry at %v", ran, retry, err, now.Add(drainRetry))
	}
	unlock()
	ran, retry, err = s.Drain(context.Background(), now)
	if err != nil || len(ran) != 1 || !retry.IsZero() {
		t.Fatalf("finished drain = %v, %v, %v; want one file and no retry", ran, retry, err)
	}
}

func TestQueuedMainHookUsesLiveMainTree(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("main", PRMerged))
	artifact := filepath.Join(repo, "node_modules", "build-output")
	if err := os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("available"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Emit(t.Context(), Event{Repo: repo, Event: PRMerged}); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "advance main")
	git(t, repo, "branch", "-m", "advanced-main")
	sha := git(t, repo, "rev-parse", "HEAD")
	called := false
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		called = true
		if p.Dir != repo {
			return fmt.Errorf("main-checkout hook ran in %s, want %s", p.Dir, repo)
		}
		if data, err := os.ReadFile(filepath.Join(p.Dir, "node_modules", "build-output")); err != nil || string(data) != "available" {
			return fmt.Errorf("main build output unavailable: %q, %v", data, err)
		}
		got, err := repoid.Git(ctx, p.Dir, "rev-parse", "HEAD")
		if err != nil || got != sha {
			return fmt.Errorf("main tree SHA=%s, want %s: %v", got, sha, err)
		}
		env := map[string]string{}
		for _, value := range p.Env {
			key, text, _ := strings.Cut(value, "=")
			env[key] = text
		}
		if env["DYNA_HOOK_SHA"] != sha || env["DYNA_HOOK_BRANCH"] != "advanced-main" {
			return fmt.Errorf("main hook metadata SHA=%s branch=%s", env["DYNA_HOOK_SHA"], env["DYNA_HOOK_BRANCH"])
		}
		return nil
	}
	if _, _, err := s.Drain(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("main hook was not called")
	}
}
