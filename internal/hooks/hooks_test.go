package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shellquote"
	statewrite "dyna-agent/internal/state"
)

func setup(t *testing.T) (Service, string) {
	t.Helper()
	// Guard subprocesses re-execute this binary. Keep race checks enabled,
	// but remove their default one-second exit grace from command completion.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	for _, v := range os.Environ() {
		key, value, _ := strings.Cut(v, "=")
		if strings.HasPrefix(key, "DYNA_") {
			t.Cleanup(func() { _ = os.Setenv(key, value) })
			_ = os.Unsetenv(key)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "initial")
	dataDir := t.TempDir()
	if err := statewrite.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	return Service{DataDir: dataDir, Post: func(Event, room.HookEvent) error { return nil }}, repo
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	v, e := repoid.Git(context.Background(), dir, args...)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func hook(name, event string) Hook {
	return Hook{Name: name, Event: event, Command: "true", Cwd: "worktree", Timeout: "30s", OnFail: "alert"}
}
func add(t *testing.T, s Service, repo string, h Hook) {
	t.Helper()
	if err := s.Add(context.Background(), repo, h); err != nil {
		t.Fatal(err)
	}
}

func TestScopeIdentityAndNoRepoTraces(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	git(t, repo, "remote", "add", "origin", "git@github.com:EXAMPLE/PROJECT.git")
	add(t, s, repo, hook("a", WorktreeCreated))
	tree := filepath.Join(t.TempDir(), "tree")
	git(t, repo, "worktree", "add", "-b", "feature", tree)
	h, err := s.List(ctx, tree)
	if err != nil || len(h) != 1 || h[0].AddedBy != "owner" || h[0].AddedAt.IsZero() {
		t.Fatalf("list: %#v %v", h, err)
	}
	git(t, repo, "remote", "set-url", "origin", "https://github.com/example/project.git")
	h, err = s.List(ctx, tree)
	if err != nil || len(h) != 1 {
		t.Fatalf("transport identity: %#v %v", h, err)
	}
	id, err := repoid.Resolve(ctx, tree)
	if err != nil {
		t.Fatal(err)
	}
	if id.ID != "github.com/example/project" || id.Main != repo {
		t.Fatalf("identity: %#v", id)
	}
	if _, err := os.Stat(filepath.Join(s.dir(id.ID), "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("created legacy copy: %v", err)
	}
	if status := git(t, repo, "status", "--porcelain", "--ignored"); status != "" {
		t.Fatalf("repo traces: %s", status)
	}
	if err := s.Remove(ctx, tree, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, tree, "a"); err == nil {
		t.Fatal("missing hook removed")
	}
}
func TestRootFallbackAndOtherRemote(t *testing.T) {
	_, repo := setup(t)
	ctx := context.Background()
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != git(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("root identity: %s", r.ID)
	}
	git(t, repo, "remote", "add", "upstream", "https://example.org/group/repo.git")
	r, err = repoid.Resolve(ctx, repo)
	if err != nil || r.ID != "example.org/group/repo" {
		t.Fatalf("other remote: %#v %v", r, err)
	}
}
func TestValidate(t *testing.T) {
	for _, change := range []func(*Hook){func(h *Hook) { h.Name = "../escape" }, func(h *Hook) { h.Event = "unknown" }, func(h *Hook) { h.Command = "" }, func(h *Hook) { h.Timeout = "0s" }, func(h *Hook) { h.Cwd = "relative" }, func(h *Hook) { h.OnFail = "block" }, func(h *Hook) { h.Event = Schedule }} {
		h := hook("check", AfterLand)
		change(&h)
		if Validate(h) == nil {
			t.Fatalf("accepted invalid hook: %#v", h)
		}
	}
	h := hook("gate", BeforeLand)
	h.OnFail = "block"
	if err := Validate(h); err != nil {
		t.Fatal(err)
	}
}
func TestRunnerEventEnvCwdAndStoredResults(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	tree := filepath.Join(t.TempDir(), "tree")
	git(t, repo, "worktree", "add", "-b", "feature", tree)
	var commands int
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		commands++
		env := strings.Join(p.Env, "\n")
		for _, want := range []string{"DYNA_HOOK_EVENT=after-land", "DYNA_HOOK_REPO=", "DYNA_HOOK_WORKTREE=" + tree, "DYNA_HOOK_BRANCH=feature", "DYNA_HOOK_SHA=event-sha", "DYNA_HOOK_PR=42", "DYNA_HOOK_SESSION=room:session", "XDG_DATA_HOME=" + os.Getenv("XDG_DATA_HOME")} {
			if !strings.Contains(env, want) {
				t.Errorf("missing %s", want)
			}
		}
		if p.Dir != repo {
			t.Errorf("cwd = %s", p.Dir)
		}
		fmt.Fprintln(p.Output, "fake command output")
		return nil
	}
	h := hook("record", AfterLand)
	h.Cwd = "main"
	add(t, s, repo, h)
	ev, err := s.Run(ctx, Event{Repo: repo, Worktree: tree, Branch: "feature", SHA: "event-sha", PR: "42", Session: "room:session"}, h.Name)
	if err != nil || ev.Result != "pass" || commands != 1 {
		t.Fatalf("run: %#v %v, commands %d", ev, err, commands)
	}
	runs, err := s.Runs(ctx, tree, h.Name)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %#v %v", runs, err)
	}
	b, err := os.ReadFile(ev.LogPath)
	if err != nil || !strings.Contains(string(b), "fake command output") {
		t.Fatalf("log: %s %v", b, err)
	}
}
func TestFailureAlertDeduplicatesCauses(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	cause := "first failure"
	alerts := 0
	s.Post = func(_ Event, ev room.HookEvent) error {
		if ev.State == room.HookRunning || ev.State == room.HookPassed {
			return nil
		}
		alerts++
		if ev.RunID == "" || ev.Repo != repo || ev.Hook != "fail" || ev.Event != AfterLand || ev.Command != "true" {
			t.Errorf("alert payload: %+v", ev)
		}
		return nil
	}
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		if cause == "success" {
			return nil
		}
		if cause == "first failure" {
			return context.DeadlineExceeded
		}
		return context.Canceled
	}
	add(t, s, repo, hook("fail", AfterLand))
	for i := 0; i < 2; i++ {
		if _, err := s.Run(ctx, Event{Repo: repo}, "fail"); err == nil {
			t.Fatal("failure lost")
		}
	}
	if alerts != 1 {
		t.Fatalf("alerts %d", alerts)
	}
	cause = "second failure"
	_, _ = s.Run(ctx, Event{Repo: repo}, "fail")
	if alerts != 2 {
		t.Fatalf("causes lost: %d", alerts)
	}
	cause = "success"
	_, _ = s.Run(ctx, Event{Repo: repo}, "fail")
	cause = "first failure"
	_, _ = s.Run(ctx, Event{Repo: repo}, "fail")
	if alerts != 3 {
		t.Fatalf("success did not reset the alert: %d", alerts)
	}
}
func TestBlockingHooksAndQueuedAlerts(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	commands := 0
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		commands++
		return errors.New("blocked")
	}
	add(t, s, repo, hook("alert", BeforeLand))
	if err := s.Emit(ctx, Event{Repo: repo, Event: BeforeLand, SHA: git(t, repo, "rev-parse", "HEAD")}); err != nil || commands != 0 || len(queuedEvents(t, s)) != 1 {
		t.Fatalf("alert delayed emitter: %v %d %d", err, commands, len(queuedEvents(t, s)))
	}
	t.Cleanup(func() {
		for _, e := range queuedEvents(t, s) {
			if e.Snapshot {
				cleanupSnapshot(e)
			}
		}
	})
	h := hook("block", BeforeLand)
	h.OnFail = "block"
	add(t, s, repo, h)
	var blocked *BlockError
	if err := s.Emit(ctx, Event{Repo: repo, Event: BeforeLand}); !errors.As(err, &blocked) || commands != 1 || !strings.Contains(err.Error(), "dyna git hooks log block --repo "+shellquote.Quote(repo)) {
		t.Fatalf("gate: %v %d", err, commands)
	}
	commands = 0
	_ = s.Dispatch(ctx, Event{Repo: repo, Event: BeforeLand})
	if commands != 1 {
		t.Fatalf("worker repeated block hook: %d", commands)
	}
}
func TestCommandTimeout(t *testing.T) {
	s, repo := setup(t)
	h := hook("timeout", AfterLand)
	h.Timeout = "100ms"
	add(t, s, repo, h)
	called := false
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		called = true
		if p.Timeout != 100*time.Millisecond {
			t.Errorf("hook command timeout = %v, want 100ms", p.Timeout)
		}
		return context.DeadlineExceeded
	}
	ev, err := s.Run(context.Background(), Event{Repo: repo}, h.Name)
	if !called || !errors.Is(err, context.DeadlineExceeded) || ev.Result != "error" {
		t.Fatalf("timeout: called=%v, run=%#v, error=%v", called, ev, err)
	}
	runs, readErr := s.Runs(context.Background(), repo, h.Name)
	if readErr != nil || len(runs) != 1 || runs[0].Result != "error" {
		t.Fatalf("stored timeout: %#v, %v", runs, readErr)
	}
}

func TestScheduleAndMainMovementClaimOnce(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	var events []Event
	s.Fetch = func(context.Context, string) error { return nil }
	h := hook("schedule", Schedule)
	h.EveryMinutes = 5
	add(t, s, repo, h)
	add(t, s, repo, hook("main", MainUpdated))
	first := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "update-ref", "refs/remotes/origin/main", first)
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Tick(ctx, repo, now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	events = queuedEvents(t, s)
	if len(events) != 1 || events[0].Event != Schedule {
		t.Fatalf("first tick: %#v", events)
	}
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "next")
	second := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "update-ref", "refs/remotes/origin/main", second)
	if err := s.Tick(ctx, repo, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx, repo, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	events = queuedEvents(t, s)
	if len(events) != 3 || events[1].Event != MainUpdated || events[1].SHA != second || events[2].Event != Schedule {
		t.Fatalf("events: %#v", events)
	}
}

func TestIntervalEditMovesThePendingRun(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	h := hook("schedule", Schedule)
	h.EveryMinutes = 60
	add(t, s, repo, h)
	first := time.Now()
	if err := s.Tick(ctx, repo, first); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 1 || events[0].Event != Schedule {
		t.Fatalf("first run: %#v", events)
	}
	// Lengthening the interval must move the pending run. The old deadline,
	// one hour after the first run, must not fire on its own.
	every := 1440
	if err := s.Edit(ctx, repo, h.Name, "owner", Patch{EveryMinutes: &every}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx, repo, first.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 1 {
		t.Fatalf("the old deadline still fired: %#v", events)
	}
	if err := s.Tick(ctx, repo, first.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 1 {
		t.Fatalf("the new interval fired early: %#v", events)
	}
	if err := s.Tick(ctx, repo, first.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 2 || events[1].Event != Schedule {
		t.Fatalf("the new interval did not fire: %#v", events)
	}
}

func TestEditBeforeFirstTickMovesALegacyRunTime(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	h := hook("schedule", Schedule)
	h.EveryMinutes = 60
	add(t, s, repo, h)
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := s.load(ctx, r.ID)
	if err != nil || len(hooks) != 1 {
		t.Fatalf("stored hook: %#v %v", hooks, err)
	}
	// An older release wrote the next run time without the interval that set
	// it. The owner lengthens the interval before the first tick here.
	first := time.Now()
	if err := s.runtime(r.ID, func(st *state) error {
		st.Due[scheduleKey(hooks[0])] = first.Add(60 * time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	every := 1440
	if err := s.Edit(ctx, repo, h.Name, "owner", Patch{EveryMinutes: &every}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx, repo, first.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 0 {
		t.Fatalf("the old deadline fired: %#v", events)
	}
	if err := s.Tick(ctx, repo, first.Add(24*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 1 || events[0].Event != Schedule {
		t.Fatalf("the new interval did not fire: %#v", events)
	}
}

func TestLegacyRunTimeWithoutAnIntervalWaitsOneInterval(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	h := hook("schedule", Schedule)
	h.EveryMinutes = 60
	add(t, s, repo, h)
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := s.load(ctx, r.ID)
	if err != nil || len(hooks) != 1 {
		t.Fatalf("stored hook: %#v %v", hooks, err)
	}
	// An older release wrote a run time with no interval provenance, and no
	// edit followed. It may predate an interval change, so it waits one
	// interval from the first tick instead of firing at that deadline.
	first := time.Now()
	if err := s.runtime(r.ID, func(st *state) error {
		st.Due[scheduleKey(hooks[0])] = first.Add(10 * time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx, repo, first.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 0 {
		t.Fatalf("an unknown old deadline fired: %#v", events)
	}
	if err := s.Tick(ctx, repo, first.Add(70*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 1 || events[0].Event != Schedule {
		t.Fatalf("the interval did not fire: %#v", events)
	}
}

func TestBeforeLandSetupErrorsBlock(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	add(t, s, repo, hook("alert", BeforeLand))
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.documentsDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE hook_documents SET body=? WHERE repo=?", []byte("broken JSON"), filepath.Base(s.dir(r.ID))); err != nil {
		t.Fatal(err)
	}
	var blocked *BlockError
	if err := s.Emit(ctx, Event{Repo: repo, Event: BeforeLand}); !errors.As(err, &blocked) {
		t.Fatalf("setup failed open: %v", err)
	}
}
func TestBeforeLandSnapshotKeepsTrialSHA(t *testing.T) {
	_, repo := setup(t)
	sha := git(t, repo, "rev-parse", "HEAD")
	event, err := snapshot(t.Context(), Event{Repo: repo, Worktree: repo, SHA: sha, Event: BeforeLand})
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, event.Worktree, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("snapshot SHA %s", got)
	}
	cleanupSnapshot(event)
	if _, err := os.Stat(event.Worktree); !os.IsNotExist(err) {
		t.Fatalf("snapshot left behind: %v", err)
	}
	if got := strings.Count(git(t, repo, "worktree", "list", "--porcelain"), "worktree "); got != 1 {
		t.Fatalf("snapshot registration left behind: %d", got)
	}
}
func TestMainUpdatedFetchesWithoutTrackedPR(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	remote := t.TempDir()
	git(t, remote, "init", "--bare")
	git(t, repo, "remote", "add", "origin", remote)
	git(t, repo, "push", "origin", "main")
	add(t, s, repo, hook("install", MainUpdated))
	var events []Event
	now := time.Now()
	if err := s.Tick(ctx, repo, now); err != nil {
		t.Fatal(err)
	}
	writer := filepath.Join(t.TempDir(), "writer")
	git(t, repo, "clone", "--branch", "main", remote, writer)
	git(t, writer, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "advance")
	next := git(t, writer, "rev-parse", "HEAD")
	git(t, writer, "push", "origin", "main")
	if err := s.Tick(ctx, repo, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	events = queuedEvents(t, s)
	if len(events) != 0 {
		t.Fatalf("fetch ignored throttle: %#v", events)
	}
	if err := s.Tick(ctx, repo, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	events = queuedEvents(t, s)
	if len(events) != 1 || events[0].SHA != next || events[0].Event != MainUpdated {
		t.Fatalf("remote movement missed: %#v", events)
	}
	repos, err := s.Repositories(t.Context())
	if err != nil || len(repos) != 1 || repos[0] != repo {
		t.Fatalf("repo registry: %#v %v", repos, err)
	}
}

func TestSpoolWriteFailureNotifies(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("queued", AfterLand))
	if err := os.WriteFile(filepath.Join(s.DataDir, "hook-spool"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	notices := 0
	s.Post = func(_ Event, ev room.HookEvent) error {
		notices++
		if ev.State != room.HookError || ev.RunID == "" || !strings.Contains(ev.Detail, "spool write error") {
			t.Errorf("spool alert: %+v", ev)
		}
		return nil
	}
	if err := s.Emit(context.Background(), Event{Repo: repo, Event: AfterLand}); err == nil || notices != 1 {
		t.Fatalf("spool failure: %v; notices %d", err, notices)
	}
}

func TestEditPreservesCreationAndRejectsInvalidPatch(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	h := hook("verify", BeforeLand)
	h.AddedBy = "room:creator"
	add(t, s, repo, h)
	before, _ := s.List(ctx, repo)
	command := "printf changed"
	if err := s.Edit(ctx, repo, h.Name, "room:editor", Patch{Command: &command}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.List(ctx, repo)
	got := after[0]
	if got.Command != command || got.AddedBy != before[0].AddedBy || got.AddedAt != before[0].AddedAt || got.EditedBy != "room:editor" || got.EditedAt == nil {
		t.Fatalf("audit: %#v", got)
	}
	if got.Cwd != h.Cwd || got.Timeout != h.Timeout || got.Event != h.Event || got.OnFail != h.OnFail {
		t.Fatalf("untouched fields changed: %#v", got)
	}
	bad := "block"
	event := AfterLand
	if err := s.Edit(ctx, repo, h.Name, "bad", Patch{Event: &event, OnFail: &bad}); err == nil {
		t.Fatal("invalid edit accepted")
	}
	unchanged, _ := s.List(ctx, repo)
	if unchanged[0].EditedBy != got.EditedBy || unchanged[0].Event != got.Event {
		t.Fatal("invalid edit persisted")
	}
	if err := s.Edit(ctx, repo, "absent", "owner", Patch{Command: &command}); err == nil {
		t.Fatal("missing hook edited")
	}
}
