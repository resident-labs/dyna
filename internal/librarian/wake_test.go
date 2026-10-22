package librarian

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/library"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/settings"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// fakeSessions records what Wake asks of the session manager. Each new
// turn ends at once with status next (ok by default), or stays running.
type fakeSessions struct {
	info   subagent.Info
	turns  []subagent.Turn
	next   string
	tasks  []string
	starts int
	closed []string
}

func (f *fakeSessions) Profiles() ([]profile.Profile, error) {
	return []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}}, nil
}
func (f *fakeSessions) Failures(time.Time) (map[string]time.Time, error) { return nil, nil }
func (f *fakeSessions) Inspect(id string) (subagent.Info, subagent.Turn, error) {
	if f.info.ID == "" || f.info.ID != id {
		return subagent.Info{}, subagent.Turn{}, os.ErrNotExist
	}
	return f.info, f.turns[len(f.turns)-1], nil
}
func (f *fakeSessions) Turn(id string, n int) (subagent.Turn, error) {
	if f.info.ID != id || n < 1 || n > len(f.turns) {
		return subagent.Turn{}, os.ErrNotExist
	}
	return f.turns[n-1], nil
}
func (f *fakeSessions) Named(roomID string) (subagent.Info, error) {
	if f.info.ID == "" || f.info.RoomID != roomID {
		return subagent.Info{}, os.ErrNotExist
	}
	return f.info, nil
}
func (f *fakeSessions) run(task string) int {
	f.tasks = append(f.tasks, task)
	status := f.next
	if status == "" {
		status = subagent.TurnOK
	}
	f.turns = append(f.turns, subagent.Turn{N: len(f.turns) + 1, Status: status})
	f.info.Turns = len(f.turns)
	f.info.Status = subagent.StatusIdle
	if status == subagent.TurnRunning {
		f.info.Status = subagent.StatusRunning
	}
	return len(f.turns)
}
func (f *fakeSessions) Start(roomID, prof, dir, goal, task string) (subagent.Info, int, error) {
	f.starts++
	f.turns = nil
	f.info = subagent.Info{Meta: subagent.Meta{ID: fmt.Sprintf("ses_%d", f.starts), RoomID: roomID, Role: subagent.RoleLibrarian, Profile: prof, Dir: dir, Resumable: true, HarnessSession: "h"}}
	n := f.run(task)
	return f.info, n, nil
}
func (f *fakeSessions) Assign(_ context.Context, id, task string) (int, error) {
	return f.run(task), nil
}
func (f *fakeSessions) Close(_ context.Context, id string) error {
	f.closed = append(f.closed, id)
	if f.info.ID == id {
		f.info = subagent.Info{}
	}
	return nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", name)
}

type harness struct {
	svc      Service
	sessions *fakeSessions
	repo     string
	id       string
	clock    time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "agents/run.go", "package agents\n\nfunc Run() {}\n")
	data := storetest.Isolate(t)
	if err := statewrite.MigrateMirrors(data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "settings.json")
	h := &harness{sessions: &fakeSessions{}, repo: repo, clock: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	h.svc = Service{DataDir: data, Sessions: h.sessions, Settings: func() (settings.Values, error) { return settings.Load(path) }, Now: func() time.Time { return h.clock }}
	configureLibrarianHooks(&h.svc, nil)
	r, err := h.svc.Enable(context.Background(), authority.Actor{Role: authority.Owner}, repo, "room_1", "/usr/bin/dyna")
	if err != nil {
		t.Fatal(err)
	}
	h.id = r.ID
	return h
}

func (h *harness) wake(t *testing.T, want string) Result {
	t.Helper()
	res, err := h.svc.Wake(context.Background(), h.repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != want {
		t.Fatalf("wake = %+v, want %s", res, want)
	}
	return res
}

func TestEnableInstallsHooksOnce(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	svc := hooks.Service{DataDir: h.svc.DataDir}
	list, err := svc.List(ctx, h.repo)
	if err != nil || len(list) != 3 {
		t.Fatalf("hooks = %+v %v", list, err)
	}
	for _, hk := range list {
		if !strings.Contains(hk.Command, "library wake --repo") || hooks.Validate(hk) != nil {
			t.Fatalf("hook %+v", hk)
		}
	}
	if _, err := h.svc.Enable(ctx, authority.Actor{Role: authority.Owner}, h.repo, "room_1", "/usr/bin/dyna"); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if again, _ := svc.List(ctx, h.repo); len(again) != 3 || !again[0].AddedAt.Equal(list[0].AddedAt) {
		t.Fatalf("second enable replaced unchanged hooks: %+v", again)
	}
}

func TestDisableRemovesHooksAndClosesTheSession(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.wake(t, "started")
	if _, err := h.svc.Disable(ctx, authority.Actor{Role: authority.Owner}, h.repo); err != nil {
		t.Fatal(err)
	}
	if list, _ := (hooks.Service{DataDir: h.svc.DataDir}).List(ctx, h.repo); len(list) != 0 {
		t.Fatalf("hooks left: %+v", list)
	}
	if len(h.sessions.closed) != 1 {
		t.Fatalf("closed = %v", h.sessions.closed)
	}
	h.wake(t, "off")
}

func TestWakeRunsOnlyWhenTheGatePasses(t *testing.T) {
	h := setup(t)
	first := h.wake(t, "started")
	if !strings.Contains(h.sessions.tasks[0], "first run") || first.Head == "" {
		t.Fatalf("first task: %s", h.sessions.tasks[0])
	}
	if _, err := os.Stat(filepath.Join(TreePath(h.svc.DataDir, h.id), "agents", "run.go")); err != nil {
		t.Fatalf("librarian tree: %v", err)
	}
	h.wake(t, "current")

	lib := library.Store{DataDir: h.svc.DataDir, Actor: authority.Current()}
	if _, err := lib.Add(h.id, library.Entry{ID: "agents", Name: "Agents", Kind: "feature", Summary: "Runs agents.", Owns: []string{"agents/**"}, VerifiedAt: git(t, h.repo, "rev-parse", "HEAD")}); err != nil {
		t.Fatal(err)
	}
	// A whitespace-only change to an owned file is skipped without a turn.
	commitFile(t, h.repo, "agents/run.go", "package agents\n\n\nfunc Run()  {}\n")
	h.clock = h.clock.Add(Interval)
	skipped := h.wake(t, "skipped")
	h.wake(t, "current")

	// A real change runs, as a follow-up turn of the same session.
	commitFile(t, h.repo, "agents/run.go", "package agents\n\nfunc Run() { panic(1) }\n")
	h.clock = h.clock.Add(Interval)
	res := h.wake(t, "assigned")
	if !strings.Contains(res.Reason, "agents/run.go") || h.sessions.starts != 1 {
		t.Fatalf("assigned %+v, starts %d", res, h.sessions.starts)
	}
	if last := h.sessions.tasks[len(h.sessions.tasks)-1]; !strings.Contains(last, "dyna library changed --since "+skipped.Head) {
		t.Fatalf("follow-up task lacks the change range:\n%s", last)
	}

	// Merges inside the interval wait; a busy session keeps them pending.
	commitFile(t, h.repo, "web/app.ts", "x")
	h.wake(t, "batched")
	h.clock = h.clock.Add(Interval)
	h.sessions.info.Status = subagent.StatusRunning
	h.wake(t, "busy")
	h.sessions.info.Status = subagent.StatusIdle
	if res := h.wake(t, "assigned"); !strings.Contains(res.Reason, "unmapped source file web/app.ts") {
		t.Fatalf("reason %q", res.Reason)
	}
}

func TestWakeIsOffUntilEnabled(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a.go", "package a")
	path := filepath.Join(t.TempDir(), "settings.json")
	svc := Service{DataDir: t.TempDir(), Sessions: &fakeSessions{}, Settings: func() (settings.Values, error) { return settings.Load(path) }}
	res, err := svc.Wake(context.Background(), repo)
	if err != nil || res.Action != "off" {
		t.Fatalf("wake = %+v %v", res, err)
	}
	if _, err := repoid.Resolve(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
}

func TestFailedTurnsRetryFromTheLastCheckpoint(t *testing.T) {
	h := setup(t)
	// A failed first turn leaves no checkpoint; the next wake starts over
	// without a new commit.
	h.sessions.next = subagent.TurnError
	h.wake(t, "started")
	h.clock = h.clock.Add(Interval)
	h.sessions.next = ""
	if res := h.wake(t, "started"); !strings.Contains(h.sessions.tasks[1], "first run") {
		t.Fatalf("retry %+v:\n%s", res, h.sessions.tasks[1])
	}
	h.clock = h.clock.Add(Interval)
	checkpoint := h.wake(t, "current").Head

	// A running turn holds the wake; a failed follow-up retries its range.
	commitFile(t, h.repo, "web/app.ts", "x")
	h.sessions.next = subagent.TurnRunning
	h.wake(t, "assigned")
	h.clock = h.clock.Add(Interval)
	h.wake(t, "busy")
	h.sessions.turns[len(h.sessions.turns)-1].Status = subagent.TurnError
	h.sessions.info.Status = subagent.StatusIdle
	h.sessions.next = ""
	h.wake(t, "started") // an errored session is recreated
	if last := h.sessions.tasks[len(h.sessions.tasks)-1]; !strings.Contains(last, "--since "+checkpoint) {
		t.Fatalf("retry lost the change range:\n%s", last)
	}
	h.clock = h.clock.Add(Interval)
	h.wake(t, "current")
}

func TestOneLibrarianPerRoom(t *testing.T) {
	h := setup(t)
	other := t.TempDir()
	git(t, other, "init", "-q", "-b", "main")
	commitFile(t, other, "b.go", "package b")
	ctx := context.Background()
	if _, err := h.svc.Enable(ctx, authority.Actor{Role: authority.Owner}, other, "room_1", "/usr/bin/dyna"); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("second repository in one room: %v", err)
	}
	b := *h
	b.repo, b.sessions = other, &fakeSessions{}
	b.svc.Sessions = b.sessions
	if _, err := b.svc.Enable(ctx, authority.Actor{Role: authority.Owner}, other, "room_2", "/usr/bin/dyna"); err != nil {
		t.Fatal(err)
	}
	h.wake(t, "started")
	b.wake(t, "started")
	if h.sessions.info.Dir == b.sessions.info.Dir || h.sessions.info.RoomID == b.sessions.info.RoomID {
		t.Fatalf("librarians share a dir or room: %+v %+v", h.sessions.info, b.sessions.info)
	}
	if _, err := b.svc.Disable(ctx, authority.Actor{Role: authority.Owner}, other); err != nil {
		t.Fatal(err)
	}
	if len(h.sessions.closed) != 0 || len(b.sessions.closed) != 1 {
		t.Fatalf("closed a=%v b=%v", h.sessions.closed, b.sessions.closed)
	}
}

// TestMovingRoomsClosesTheOldSession shares one session registry between two
// repositories. After A moves to room 2, B in room 1 must not adopt A's
// session through the named lookup.
func TestMovingRoomsClosesTheOldSession(t *testing.T) {
	a := setup(t)
	ctx := context.Background()
	a.wake(t, "started")
	oldID := a.sessions.info.ID
	if _, err := a.svc.Enable(ctx, authority.Actor{Role: authority.Owner}, a.repo, "room_2", "/usr/bin/dyna"); err != nil {
		t.Fatal(err)
	}
	if len(a.sessions.closed) != 1 || a.sessions.closed[0] != oldID {
		t.Fatalf("closed = %v, want %s", a.sessions.closed, oldID)
	}
	other := t.TempDir()
	git(t, other, "init", "-q", "-b", "main")
	commitFile(t, other, "b.go", "package b")
	if _, err := a.svc.Enable(ctx, authority.Actor{Role: authority.Owner}, other, "room_1", "/usr/bin/dyna"); err != nil {
		t.Fatal(err)
	}
	b := *a
	b.repo = other
	b.wake(t, "started")
	if a.sessions.info.ID == oldID || !strings.Contains(a.sessions.tasks[len(a.sessions.tasks)-1], other) {
		t.Fatalf("B reused A's session %s", oldID)
	}
}

// The gate also runs the librarian for a pin left stale by an earlier run,
// but not for a whitespace-only change since the pin.
func TestGateRunsForStalePins(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "agents/run.go", "package agents\n\nfunc Run() {}\n")
	pin := git(t, repo, "rev-parse", "HEAD")
	commitFile(t, repo, "agents/run.go", "package agents\n\nfunc Run() { panic(1) }\n")
	base := git(t, repo, "rev-parse", "HEAD")
	commitFile(t, repo, "README.md", "notes")
	target := git(t, repo, "rev-parse", "HEAD")
	agents := library.Entry{ID: "agents", Owns: []string{"agents/**"}, VerifiedAt: pin}
	ctx := context.Background()
	// base..target touches only the README, but the agents pin predates a
	// real change to its file.
	if reason, err := Gate(ctx, repo, base, target, []library.Entry{agents}); err != nil || reason != "stale pins on agents" {
		t.Fatalf("stale pin: %q %v", reason, err)
	}
	agents.VerifiedAt = base
	if reason, err := Gate(ctx, repo, base, target, []library.Entry{agents}); err != nil || reason != "" {
		t.Fatalf("fresh pin: %q %v", reason, err)
	}
	commitFile(t, repo, "agents/run.go", "package agents\n\n\nfunc Run() { panic(1) }\n")
	ws := git(t, repo, "rev-parse", "HEAD")
	if reason, err := Gate(ctx, repo, target, ws, []library.Entry{agents}); err != nil || reason != "" {
		t.Fatalf("whitespace since the pin: %q %v", reason, err)
	}
}
