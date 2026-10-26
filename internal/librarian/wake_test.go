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
	"dyna-agent/internal/librarianstate"
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
	return []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}, {Name: "diagnostic", Harness: profile.HarnessMock}}, nil
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
func (f *fakeSessions) Named(roomID string, _ bool) (subagent.Info, error) {
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
func (f *fakeSessions) Start(roomID, prof, dir, goal, task string, smell bool) (subagent.Info, int, error) {
	f.starts++
	f.turns = nil
	name := subagent.LibrarianName
	if smell {
		name = subagent.LibrarianSmellName
	}
	f.info = subagent.Info{Meta: subagent.Meta{ID: fmt.Sprintf("ses_%d", f.starts), Name: name, RoomID: roomID, Role: subagent.RoleLibrarian, Profile: prof, Dir: dir, Resumable: true, HarnessSession: "h"}}
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
	if _, err := (librarianstate.Store{DataDir: data}).Update(h.id, false, func(st *librarianstate.State) error {
		st.SmellHead, st.SmellLastRun = git(t, repo, "rev-parse", "HEAD"), h.clock
		return nil
	}); err != nil {
		t.Fatal(err)
	}
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

func TestSmellWakeUsesItsOwnCheckpointAndProfile(t *testing.T) {
	h := setup(t)
	h.svc.Settings = func() (settings.Values, error) {
		return settings.Values{LibraryEnabled: []string{h.repo}, LibrarySmellProfile: "diagnostic"}, nil
	}
	baseline := git(t, h.repo, "rev-parse", "HEAD")
	if _, err := (librarianstate.Store{DataDir: h.svc.DataDir}).Update(h.id, false, func(st *librarianstate.State) error {
		st.SmellHead, st.SmellLastRun = baseline, h.clock
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commitFile(t, h.repo, "agents/run.go", "package agents\n\nfunc Run() { panic(1) }\n")
	target := git(t, h.repo, "rev-parse", "HEAD")
	if _, err := (librarianstate.Store{DataDir: h.svc.DataDir}).Update(h.id, false, func(st *librarianstate.State) error { st.Head = target; return nil }); err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(SmellInterval)
	res, err := h.svc.Wake(context.Background(), h.repo)
	if err != nil || res.Action != "started" {
		t.Fatalf("smell wake = %+v %v", res, err)
	}
	task := h.sessions.tasks[len(h.sessions.tasks)-1]
	if !strings.Contains(task, baseline+".."+res.Head) || !strings.Contains(task, "Read each full changed file") {
		t.Fatalf("smell task lacks its change range or code-reading assignment:\n%s", task)
	}
	if h.sessions.info.Name != subagent.LibrarianSmellName || h.sessions.info.Profile != "diagnostic" {
		t.Fatalf("smell session = %q with profile %q", h.sessions.info.Name, h.sessions.info.Profile)
	}
	if _, err := os.Stat(filepath.Join(SmellTreePath(h.svc.DataDir, h.id), "agents", "run.go")); err != nil {
		t.Fatalf("smell tree: %v", err)
	}
	h.clock = h.clock.Add(time.Minute)
	if res, err = h.svc.Wake(context.Background(), h.repo); err != nil || res.Action != "current" {
		t.Fatalf("completed smell checkpoint = %+v %v", res, err)
	}
	commitFile(t, h.repo, "agents/run.go", "package agents\n\nfunc Run() {}\n")
	if _, err := (librarianstate.Store{DataDir: h.svc.DataDir}).Update(h.id, false, func(st *librarianstate.State) error { st.Head = git(t, h.repo, "rev-parse", "HEAD"); return nil }); err != nil {
		t.Fatal(err)
	}
	tasks := len(h.sessions.tasks)
	if res, err = h.svc.Wake(context.Background(), h.repo); err != nil || res.Action != "current" || len(h.sessions.tasks) != tasks {
		t.Fatalf("smell pass inside interval = %+v %v, tasks %d -> %d", res, err, tasks, len(h.sessions.tasks))
	}
}

func TestSmellWakeNowSkipsTheIntervalForOwnerAndRootOnly(t *testing.T) {
	h := setup(t)
	baseline := git(t, h.repo, "rev-parse", "HEAD")
	if _, err := (librarianstate.Store{DataDir: h.svc.DataDir}).Update(h.id, false, func(st *librarianstate.State) error {
		st.SmellHead, st.SmellLastRun = baseline, h.clock
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commitFile(t, h.repo, "agents/now.go", "package agents\n")
	h.clock = h.clock.Add(time.Minute)
	ctx := context.Background()
	saved, err := (librarianstate.Store{DataDir: h.svc.DataDir}).Get(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.WakeSmellsNow(ctx, authority.Actor{Role: authority.Root}, "room_other", h.repo); err == nil || !strings.Contains(err.Error(), "another room") {
		t.Fatalf("root of another room wake now = %v, want a cross-room refusal", err)
	}
	if res, err := h.svc.WakeSmellsNow(ctx, authority.Actor{Role: authority.Owner}, "", h.repo); err != nil || res.Action != "started" {
		t.Fatalf("owner wake now = %+v %v, want started", res, err)
	}
	h.clock = h.clock.Add(time.Minute)
	h.sessions.turns[len(h.sessions.turns)-1].Status = subagent.TurnOK
	commitFile(t, h.repo, "agents/now2.go", "package agents\n")
	if res, err := h.svc.wakeSmells(ctx, h.repo, false); err != nil || res.Action != "batched" {
		t.Fatalf("scheduled smell wake inside the interval = %+v %v, want batched", res, err)
	}
	if _, err := h.svc.WakeSmellsNow(ctx, authority.Actor{Role: authority.Session}, "", h.repo); err == nil || !strings.Contains(err.Error(), "owner or root only") {
		t.Fatalf("session wake now = %v, want an owner or root error", err)
	}
	if res, err := h.svc.WakeSmellsNow(ctx, authority.Actor{Role: authority.Root}, saved.RoomID, h.repo); err != nil || (res.Action != "started" && res.Action != "assigned") {
		t.Fatalf("root wake now inside the interval = %+v %v, want a started pass", res, err)
	}
	if st, err := (librarianstate.Store{DataDir: h.svc.DataDir}).Get(h.id); err != nil || !st.SmellUncapped {
		t.Fatalf("state after wake now = %+v %v, want the pass marked uncapped", st, err)
	}
	// The first pass is still on its last turn, so a second --now stays busy.
	h.sessions.turns[len(h.sessions.turns)-1].Status = subagent.TurnRunning
	tasks := len(h.sessions.tasks)
	if res, err := h.svc.WakeSmellsNow(ctx, authority.Actor{Role: authority.Root}, saved.RoomID, h.repo); err != nil || res.Action != "busy" || len(h.sessions.tasks) != tasks {
		t.Fatalf("wake now while a smell turn runs = %+v %v, want busy and no new task", res, err)
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

// The librarian follows the default branch the checkout is on, so a fork whose
// default is trunk does not read a stale origin/main left by an earlier fetch.
func TestMainHeadFollowsTheDefaultBranch(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "trunk")
	commitFile(t, repo, "agents/run.go", "package agents\n\nfunc Run() {}\n")
	stale := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "update-ref", "refs/remotes/origin/main", stale)
	commitFile(t, repo, "agents/run.go", "package agents\n\nfunc Run() { _ = 1 }\n")
	trunk := git(t, repo, "rev-parse", "HEAD")
	ctx := context.Background()
	if got, err := MainHead(ctx, repo); err != nil || got != trunk {
		t.Fatalf("trunk head = %s, %v; want %s", got, err, trunk)
	}
	git(t, repo, "update-ref", "refs/remotes/origin/trunk", trunk)
	git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	git(t, repo, "checkout", "-q", "--detach", stale)
	if got, err := MainHead(ctx, repo); err != nil || got != trunk {
		t.Fatalf("detached origin/HEAD = %s, %v; want %s", got, err, trunk)
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
